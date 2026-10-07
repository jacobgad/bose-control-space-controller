package design

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoDesign is returned by Store.Load before any project file has been saved.
var ErrNoDesign = errors.New("design: no project file has been uploaded yet")

// Store keeps the uploaded project file under Dir. The single .csp present there,
// under its original name, is the design the add-on runs.
type Store struct {
	Dir string
}

// Load parses the stored project file and reports its file name.
func (s Store) Load() (Design, string, error) {
	path, err := s.current()
	if err != nil {
		return Design{}, "", err
	}
	d, err := LoadFile(path)
	if err != nil {
		return Design{}, filepath.Base(path), err
	}
	return d, filepath.Base(path), nil
}

// Save parses data and, only if it is a valid design, replaces whatever file was
// stored before, reporting the name it was stored under. A rejected upload never
// disturbs the running design.
func (s Store) Save(name string, data []byte) (Design, string, error) {
	d, err := Parse(bytes.NewReader(data))
	if err != nil {
		return Design{}, "", err
	}
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return Design{}, "", err
	}
	stored := safeName(name)
	target := filepath.Join(s.Dir, stored)
	tmp, err := os.CreateTemp(s.Dir, ".upload-*")
	if err != nil {
		return Design{}, "", err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return Design{}, "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return Design{}, "", err
	}
	previous, _ := s.current()
	if err := os.Rename(tmp.Name(), target); err != nil {
		_ = os.Remove(tmp.Name())
		return Design{}, "", err
	}
	if previous != "" && previous != target {
		_ = os.Remove(previous)
	}
	return d, stored, nil
}

func (s Store) current() (string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var matches []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".csp") {
			matches = append(matches, filepath.Join(s.Dir, e.Name()))
		}
	}
	switch len(matches) {
	case 0:
		return "", ErrNoDesign
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("design: %d project files in %s, expected one", len(matches), s.Dir)
	}
}

func safeName(name string) string {
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	base = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '/' || r == 0x7f {
			return -1
		}
		return r
	}, base)
	if base == "" || base == "." || base == ".." || strings.HasPrefix(base, ".") {
		base = "design.csp"
	}
	if !strings.EqualFold(filepath.Ext(base), ".csp") {
		base += ".csp"
	}
	return base
}
