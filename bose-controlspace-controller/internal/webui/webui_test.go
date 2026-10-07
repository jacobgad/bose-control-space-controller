package webui_test

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/design"
	"github.com/jacobgad/bose-control-space-controller/internal/webui"
)

type app struct {
	status   webui.Status
	uploaded []string
	reject   error
}

func (a *app) deps() webui.Deps {
	return webui.Deps{
		Status: func() webui.Status { return a.status },
		Upload: func(name string, data []byte) error {
			if a.reject != nil {
				return a.reject
			}
			a.uploaded = append(a.uploaded, name+":"+string(data))
			return nil
		},
	}
}

func running() webui.Status {
	return webui.Status{
		File:     "hall-v3.csp",
		LoadedAt: time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC),
		Design: &design.Design{
			CreatedBy: "5.14.2.7",
			Devices: []design.Device{
				{NodeID: "100001", Label: "ESP Main", Model: "ESP-880", IP: netip.MustParseAddr("192.0.2.10"), Port: 10055, Firmware: "v4.100", IsMain: true},
				{NodeID: "100003", Label: "Amp Main", Model: "PM8500N", IP: netip.MustParseAddr("192.0.2.20"), Port: 10055, Firmware: "v4.200"},
			},
			Blocks:        make([]design.Block, 7),
			ParameterSets: make([]design.ParameterSet, 3),
		},
	}
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func postFile(t *testing.T, h http.Handler, name, content string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("design", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, content)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIndexShowsRunningDesign(t *testing.T) {
	t.Parallel()
	a := &app{status: running()}
	rec := get(t, webui.Handler(a.deps()), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"hall-v3.csp", "5.14.2.7", "ESP Main", "(main)", "192.0.2.10:10055", "PM8500N", "v4.200", `action="upload"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "No design is running") {
		t.Error("page claims nothing is running")
	}
}

func TestIndexExplainsWhyNothingIsRunning(t *testing.T) {
	t.Parallel()
	a := &app{status: webui.Status{Err: errors.New("design: no project file has been uploaded yet")}}
	body := get(t, webui.Handler(a.deps()), "/").Body.String()
	if !strings.Contains(body, "No design is running") || !strings.Contains(body, "no project file has been uploaded yet") {
		t.Fatalf("page = %s", body)
	}
}

func TestUploadHandsFileToApplicationAndRedirects(t *testing.T) {
	t.Parallel()
	a := &app{status: running()}
	rec := postFile(t, webui.Handler(a.deps()), "Hall v4.csp", "<Project/>")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "./?saved" {
		t.Fatalf("status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(a.uploaded) != 1 || a.uploaded[0] != "Hall v4.csp:<Project/>" {
		t.Fatalf("uploaded = %v", a.uploaded)
	}
	if body := get(t, webui.Handler(a.deps()), "/?saved").Body.String(); !strings.Contains(body, "Design saved and applied") {
		t.Fatal("confirmation missing after redirect")
	}
}

func TestRejectedUploadIsReportedOnThePage(t *testing.T) {
	t.Parallel()
	a := &app{status: running(), reject: errors.New("design: no ESP or PowerMatch devices found")}
	rec := postFile(t, webui.Handler(a.deps()), "empty.csp", "<Project/>")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "no ESP or PowerMatch devices found") || !strings.Contains(body, "hall-v3.csp") {
		t.Fatalf("page = %s", body)
	}
}

func TestUploadWithoutFileIsBadRequest(t *testing.T) {
	t.Parallel()
	a := &app{}
	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("design=nothing"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	webui.Handler(a.deps()).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || len(a.uploaded) != 0 {
		t.Fatalf("status %d uploaded %v", rec.Code, a.uploaded)
	}
}

func TestOnlyTheIngressProxyIsServedWhenRestricted(t *testing.T) {
	t.Parallel()
	a := &app{status: running()}
	deps := a.deps()
	deps.AllowedRemote = "172.30.32.2"
	h := webui.Handler(deps)

	for addr, want := range map[string]int{"172.30.32.2:4321": http.StatusOK, "172.30.33.9:4321": http.StatusForbidden} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", addr, rec.Code, want)
		}
	}
}
