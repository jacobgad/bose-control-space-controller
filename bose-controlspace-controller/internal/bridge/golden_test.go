package bridge_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite golden discovery payloads")

func TestDiscoveryPayloadsMatchGolden(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.start(t)

	configs := h.mqtt.DiscoveryConfigs()
	topics := make([]string, 0, len(configs))
	for topic := range configs {
		topics = append(topics, topic)
	}
	sort.Strings(topics)
	snapshot := make([]map[string]any, 0, len(topics))
	for _, topic := range topics {
		snapshot = append(snapshot, map[string]any{"topic": topic, "payload": configs[topic]})
	}
	got, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	path := filepath.Join("testdata", "discovery.golden.json")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file; run with -update: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("discovery payloads differ from %s (run `go test ./internal/bridge -run Golden -update` after an intentional change)", path)
	}
}
