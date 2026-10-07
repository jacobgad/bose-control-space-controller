package capture_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/capture"
	"github.com/jacobgad/bose-control-space-controller/internal/csp"
	"github.com/jacobgad/bose-control-space-controller/internal/design"
	"github.com/jacobgad/bose-control-space-controller/internal/testutil"
)

func TestCaptureWritesTranscriptsAndSummary(t *testing.T) {
	t.Parallel()
	d, err := design.LoadFile(filepath.Join("..", "design", "testdata", "hall-v2.xml"))
	if err != nil {
		t.Fatal(err)
	}
	devices := map[string]*testutil.FakeDevice{}
	for _, dev := range d.Devices {
		fake := testutil.NewFakeDevice()
		fake.Subscriptions = dev.Type == design.DeviceESP
		for _, blk := range d.BlocksOn(dev.NodeID) {
			fake.SetModule(blk.Label, 1, "-3.0")
			fake.SetModule(blk.Label, 2, "F")
			fake.SetModule(blk.Label, 3, "-3.0")
			fake.SetModule(blk.Label, 4, "F")
			fake.SetModule(blk.Label, 5, "O")
		}
		devices[dev.Address()] = fake
	}
	unreachable := "192.0.2.21:10056"
	dial := func(ctx context.Context, address string) (net.Conn, error) {
		if address == unreachable {
			return nil, &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}
		}
		return devices[address].Dial(ctx, address)
	}
	dir := t.TempDir()
	results, err := capture.Run(t.Context(), capture.Options{
		Design: d, Dir: dir, Dial: dial, Timeout: 100 * time.Millisecond,
		Now: func() time.Time { return time.Date(2026, 4, 8, 9, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("results = %d", len(results))
	}
	byDevice := map[string]capture.Result{}
	for _, r := range results {
		byDevice[r.Device] = r
	}
	esp := byDevice["ESP Main"]
	if !esp.Reachable || esp.Subscriptions != "SUB yes" || esp.Commands == 0 || esp.Responses == 0 {
		t.Fatalf("ESP Main = %+v", esp)
	}
	amp := byDevice["Amp Main"]
	if !amp.Reachable || amp.Subscriptions != "no response" || amp.Timeouts < 4 {
		t.Fatalf("Amp Main = %+v", amp)
	}
	if byDevice["Amp Annex"].Reachable || byDevice["Amp Annex"].Error == "" {
		t.Fatalf("Amp Annex = %+v", byDevice["Amp Annex"])
	}

	runDir := filepath.Join(dir, "2026-04-08T09-30-00Z")
	summary, err := os.ReadFile(filepath.Join(runDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded []capture.Result
	if err := json.Unmarshal(summary, &decoded); err != nil {
		t.Fatal(err)
	}
	transcript, err := os.ReadFile(filepath.Join(runDir, "ESP_Main-100001.log"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(transcript)
	for _, want := range []string{"> SUB", "< 53 55 42 20 79 65 73 0d  |SUB yes\\r|", `> GA "Hall">1`, `|GA"Hall">1=-3.0\r|`, `> SA "`, "|<ACK>|", "> GS"} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript missing %q", want)
		}
	}
	if strings.Contains(text, "<NAK>") {
		t.Error("no NAKs expected from a fully seeded device")
	}
	secondary := string(mustRead(t, filepath.Join(runDir, "ESP_Annex-100002.log")))
	if strings.Contains(secondary, "> "+csp.GetParameterSet+"\n") {
		t.Error("GS must only be sent to the main device")
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(runDir, "Amp_Main-100003.log"))), "> GC") {
		t.Error("amp transcript missing GC probe")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
