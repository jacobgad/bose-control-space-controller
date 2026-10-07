package design_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jacobgad/bose-control-space-controller/internal/design"
)

func TestStoreLoadWithoutUploadReportsNoDesign(t *testing.T) {
	t.Parallel()
	_, _, err := design.Store{Dir: filepath.Join(t.TempDir(), "missing")}.Load()
	if !errors.Is(err, design.ErrNoDesign) {
		t.Fatalf("err = %v, want ErrNoDesign", err)
	}
}

func TestStoreSaveThenLoadRoundTrips(t *testing.T) {
	t.Parallel()
	s := design.Store{Dir: filepath.Join(t.TempDir(), "design")}
	data, _ := os.ReadFile(filepath.Join("testdata", currentFixture))

	saved, stored, err := s.Save("Hall_Design_v2.0.0.csp", data)
	if err != nil {
		t.Fatal(err)
	}
	loaded, name, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if stored != name || name != "Hall_Design_v2.0.0.csp" || len(loaded.Devices) != len(saved.Devices) || loaded.Key != saved.Key {
		t.Fatalf("loaded %q with %d devices, saved %d", name, len(loaded.Devices), len(saved.Devices))
	}
}

func TestStoreSaveReplacesPreviousFile(t *testing.T) {
	t.Parallel()
	s := design.Store{Dir: t.TempDir()}
	first, _ := os.ReadFile(filepath.Join("testdata", currentFixture))
	second, _ := os.ReadFile(filepath.Join("testdata", previousFixture))

	if _, _, err := s.Save("v2.csp", first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Save("latest.csp", second); err != nil {
		t.Fatal(err)
	}
	d, name, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if name != "latest.csp" || d.CreatedBy != "5.13.0.4" {
		t.Fatalf("current = %q created by %q", name, d.CreatedBy)
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 1 {
		t.Fatalf("store holds %d files, want 1", len(entries))
	}
}

func TestStoreRejectsInvalidUploadAndKeepsCurrent(t *testing.T) {
	t.Parallel()
	s := design.Store{Dir: t.TempDir()}
	data, _ := os.ReadFile(filepath.Join("testdata", currentFixture))
	if _, _, err := s.Save("good.csp", data); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Save("bad.csp", []byte("<Project></Project>")); err == nil {
		t.Fatal("design without devices must be rejected")
	}
	if _, _, err := s.Save("bad.csp", []byte("not xml")); err == nil {
		t.Fatal("non-xml must be rejected")
	}
	_, name, err := s.Load()
	if err != nil || name != "good.csp" {
		t.Fatalf("current = %q, %v", name, err)
	}
}

func TestStoreSanitisesUploadedName(t *testing.T) {
	t.Parallel()
	s := design.Store{Dir: t.TempDir()}
	data, _ := os.ReadFile(filepath.Join("testdata", currentFixture))
	cases := map[string]string{
		`C:\Users\me\Desktop\Hall v3.csp`: "Hall v3.csp",
		"../../etc/passwd":                "passwd.csp",
		"":                                "design.csp",
		".hidden":                         "design.csp",
		"export.CSP":                      "export.CSP",
	}
	for in, want := range cases {
		if _, _, err := s.Save(in, data); err != nil {
			t.Fatal(err)
		}
		_, name, err := s.Load()
		if err != nil || name != want {
			t.Errorf("Save(%q) stored %q (%v), want %q", in, name, err, want)
		}
	}
}
