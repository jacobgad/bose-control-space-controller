package bridge_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/bridge"
	"github.com/jacobgad/bose-control-space-controller/internal/config"
	"github.com/jacobgad/bose-control-space-controller/internal/csp"
	"github.com/jacobgad/bose-control-space-controller/internal/design"
	mqttpkg "github.com/jacobgad/bose-control-space-controller/internal/mqtt"
	"github.com/jacobgad/bose-control-space-controller/internal/testutil"
)

const (
	espMain     = "100001"
	espAnnex    = "100002"
	ampMain     = "100003"
	hallGain    = "200001"
	foyerGain   = "200002"
	annexGain   = "200004"
	annexMic    = "300004"
	hallAmp     = "500001"
	retiredGain = "200009"
)

var designPath = filepath.Join("..", "design", "testdata", "hall-v2.xml")

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Contains(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Contains(b.buf.String(), s)
}

func (b *lockedBuffer) Count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), s)
}

type harness struct {
	design   design.Design
	mqtt     *testutil.FakeMQTT
	devices  map[string]*testutil.FakeDevice
	manifest *bridge.MemoryManifest
	bridge   *bridge.Bridge
	logs     *lockedBuffer
	ctx      context.Context
	fixed    time.Time
}

type harnessOptions struct {
	manifest *bridge.MemoryManifest
	debounce time.Duration
	unreach  map[string]bool
}

func newHarness(t *testing.T, o harnessOptions) *harness {
	t.Helper()
	d, err := design.LoadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		design:   d,
		mqtt:     testutil.NewFakeMQTT(true),
		devices:  make(map[string]*testutil.FakeDevice),
		manifest: o.manifest,
		logs:     &lockedBuffer{},
		ctx:      t.Context(),
		fixed:    time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC),
	}
	if h.manifest == nil {
		h.manifest = &bridge.MemoryManifest{}
	}
	for _, dev := range d.Devices {
		fake := testutil.NewFakeDevice()
		fake.ValueTerminator = ";"
		for _, blk := range d.BlocksOn(dev.NodeID) {
			seed(fake, blk)
		}
		h.devices[dev.NodeID] = fake
	}
	dial := func(ctx context.Context, address string) (net.Conn, error) {
		for _, dev := range d.Devices {
			if dev.Address() == address {
				if o.unreach[dev.NodeID] {
					return nil, &net.OpError{Op: "dial", Err: errRefused}
				}
				return h.devices[dev.NodeID].Dial(ctx, address)
			}
		}
		t.Fatalf("dial to unknown address %s", address)
		return nil, nil
	}
	h.bridge = bridge.New(bridge.Deps{
		Design:        d,
		DesignFile:    filepath.Base(designPath),
		MQTT:          h.mqtt,
		Dial:          dial,
		Options:       config.Options{PollInterval: 30 * time.Millisecond, WriteDebounce: o.debounce, LogLevel: slog.LevelDebug},
		Log:           slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Origin:        mqttpkg.Origin{Version: "test", SupportURL: "https://example.invalid"},
		Manifest:      h.manifest,
		DeviceTimeout: 200 * time.Millisecond,
		Now:           func() time.Time { return h.fixed },
	})
	return h
}

type refusedError struct{}

func (refusedError) Error() string { return "connection refused" }

var errRefused = refusedError{}

func seed(dev *testutil.FakeDevice, blk design.Block) {
	switch blk.Kind {
	case design.KindGain:
		dev.SetModule(blk.Label, csp.GainLevel, "-2.0")
		dev.SetModule(blk.Label, csp.GainMute, "F")
	case design.KindInput:
		dev.SetModule(blk.Label, csp.InputLevel, "0.0")
		dev.SetModule(blk.Label, csp.InputMute, "F")
		dev.SetModule(blk.Label, csp.InputPhantom, "O")
	case design.KindAmpOutput:
		dev.SetModule(blk.Label, csp.AmpOutputLevel, "-16.0")
		dev.SetModule(blk.Label, csp.AmpOutputMute, "F")
	}
}

func (h *harness) start(t *testing.T) {
	t.Helper()
	if err := h.bridge.Start(h.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		h.bridge.Stop(ctx)
	})
}

func (h *harness) device(nodeID string) *testutil.FakeDevice { return h.devices[nodeID] }

func (h *harness) block(nodeID string) mqttpkg.BlockTopics { return mqttpkg.ForBlock(nodeID) }

func eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) waitPayload(t *testing.T, topic, want string) {
	t.Helper()
	eventually(t, func() bool { return h.mqtt.LastPayload(topic) == want }, topic+" == "+want+" (last: "+h.mqtt.LastPayload(topic)+")")
}

func availabilityTopics(config map[string]any) []string {
	entries, _ := config["availability"].([]any)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		m, _ := e.(map[string]any)
		out = append(out, m["topic"].(string))
	}
	return out
}

func countCommands(cmds []string, prefix string) int {
	n := 0
	for _, c := range cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}
