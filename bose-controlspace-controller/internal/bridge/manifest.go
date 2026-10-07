package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Manifest remembers which discovery topics were published, so a later start with a
// different design can delete the ones that no longer exist.
type Manifest interface {
	Load() ([]string, error)
	Save(topics []string) error
}

// FileManifest stores the topic list as JSON.
type FileManifest struct {
	Path string
}

type manifestFile struct {
	Topics []string `json:"topics"`
}

// Load reads the previous topic list; a missing file is an empty list.
func (m FileManifest) Load() ([]string, error) {
	data, err := os.ReadFile(m.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f manifestFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", m.Path, err)
	}
	return f.Topics, nil
}

// Save writes the topic list atomically.
func (m FileManifest) Save(topics []string) error {
	sorted := append([]string(nil), topics...)
	sort.Strings(sorted)
	data, err := json.MarshalIndent(manifestFile{Topics: sorted}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.Path), 0o750); err != nil {
		return err
	}
	tmp := m.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.Path)
}

// MemoryManifest keeps the list in memory.
type MemoryManifest struct {
	Topics []string
}

// Load returns the stored list.
func (m *MemoryManifest) Load() ([]string, error) { return append([]string(nil), m.Topics...), nil }

// Save replaces the stored list.
func (m *MemoryManifest) Save(topics []string) error {
	m.Topics = append([]string(nil), topics...)
	return nil
}
