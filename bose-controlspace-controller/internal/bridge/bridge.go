// Package bridge wires a parsed design to the devices and the broker: it publishes
// discovery, polls device state into MQTT and turns MQTT commands into protocol writes.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/config"
	"github.com/jacobgad/bose-control-space-controller/internal/csp"
	"github.com/jacobgad/bose-control-space-controller/internal/design"
	"github.com/jacobgad/bose-control-space-controller/internal/mqtt"
)

// Deps are the bridge's collaborators.
type Deps struct {
	Design     design.Design
	DesignFile string
	MQTT       mqtt.Connection
	Dial       csp.Dialer
	Options    config.Options
	Log        *slog.Logger
	Origin     mqtt.Origin
	Manifest   Manifest
	// DeviceTimeout bounds each protocol request; zero uses the client default.
	DeviceTimeout time.Duration
	// Now is the clock for timestamps; nil uses time.Now.
	Now func() time.Time
}

// Bridge is the running add-on.
type Bridge struct {
	deps    Deps
	log     *slog.Logger
	devices map[string]*unit
	blocks  map[string]*block
	sets    map[int]design.ParameterSet
	main    *unit

	states  *stateCache
	pending *debouncer

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type unit struct {
	design.Device
	client *csp.Client
	blocks []*block
	topics mqtt.DeviceTopics

	mu        sync.Mutex
	connected bool
	lastError string
	since     time.Time
}

type block struct {
	design.Block
	unit   *unit
	topics mqtt.BlockTopics

	mu     sync.Mutex
	failed map[string]bool
}

// New prepares a bridge; nothing is contacted until Start.
func New(deps Deps) *Bridge {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Manifest == nil {
		deps.Manifest = &MemoryManifest{}
	}
	b := &Bridge{
		deps:    deps,
		log:     deps.Log,
		devices: make(map[string]*unit),
		blocks:  make(map[string]*block),
		sets:    make(map[int]design.ParameterSet),
		states:  newStateCache(),
	}
	b.pending = newDebouncer(deps.Options.WriteDebounce)
	for _, d := range deps.Design.Devices {
		u := &unit{Device: d, topics: mqtt.ForDevice(d.NodeID)}
		b.devices[d.NodeID] = u
		if d.IsMain {
			b.main = u
		}
	}
	for _, blk := range deps.Design.Blocks {
		u, ok := b.devices[blk.DeviceNodeID]
		if !ok {
			continue
		}
		bl := &block{Block: blk, unit: u, topics: mqtt.ForBlock(blk.NodeID), failed: make(map[string]bool)}
		b.blocks[blk.NodeID] = bl
		u.blocks = append(u.blocks, bl)
	}
	for _, ps := range deps.Design.ParameterSets {
		b.sets[ps.ID] = ps
	}
	return b
}

// Start publishes discovery, subscribes to commands and begins polling.
func (b *Bridge) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(context.WithoutCancel(ctx))

	for _, u := range b.devices {
		u.client = csp.NewClient(csp.ClientOptions{
			Address: u.Address(),
			Dial:    b.deps.Dial,
			Timeout: b.deps.DeviceTimeout,
			Log:     b.log.With("device", u.Label),
			OnState: b.stateHandler(u),
		})
	}

	b.deps.MQTT.OnMessage(mqtt.NewRouter(mqtt.Actions{
		LevelCommand:        b.LevelCommand,
		MuteCommand:         b.MuteCommand,
		RecallParameterSet:  b.RecallParameterSet,
		HomeAssistantOnline: b.republish,
	}, b.log))
	b.deps.MQTT.OnConnect(b.republish)

	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := b.deps.MQTT.AwaitConnection(waitCtx); err != nil {
		return fmt.Errorf("mqtt: broker not reachable: %w", err)
	}
	if err := b.deps.MQTT.Subscribe(ctx, mqtt.Subscriptions); err != nil {
		return fmt.Errorf("mqtt: subscribe: %w", err)
	}
	if err := b.publishDiscovery(ctx); err != nil {
		return err
	}
	b.publishAvailability(ctx, mqtt.ControllerAvailability, true)
	b.publishDesign(ctx)
	for _, u := range b.devices {
		b.publishUnitState(ctx, u)
	}
	b.logDesign()

	for _, u := range b.devices {
		b.wg.Add(1)
		go b.pollLoop(u)
	}
	return nil
}

// Stop ends polling, marks everything offline and hangs up.
func (b *Bridge) Stop(ctx context.Context) {
	if b.cancel != nil {
		b.cancel()
	}
	b.pending.stop()
	b.wg.Wait()
	for _, u := range b.devices {
		if u.client != nil {
			u.client.Close()
		}
		b.publishAvailability(ctx, u.topics.Availability, false)
	}
	b.publishAvailability(ctx, mqtt.ControllerAvailability, false)
	if err := b.deps.MQTT.Close(ctx); err != nil {
		b.log.Warn("mqtt_close_failed", "error", err.Error())
	}
}

func (b *Bridge) logDesign() {
	d := b.deps.Design
	b.log.Info("design_loaded", "file", b.deps.DesignFile, "created_by", d.CreatedBy, "devices", len(d.Devices), "blocks", len(d.Blocks), "parameter_sets", len(d.ParameterSets))
	for _, s := range d.Skipped {
		b.log.Warn("block_skipped", "node_id", s.NodeID, "label", s.Label, "reason", s.Reason)
	}
	if b.main == nil {
		b.log.Warn("no_main_device", "detail", "parameter sets cannot be recalled without a device flagged as main")
	}
}

func (b *Bridge) publishDesign(ctx context.Context) {
	d := b.deps.Design
	attrs, _ := json.Marshal(map[string]any{
		"file":           b.deps.DesignFile,
		"created_by":     d.CreatedBy,
		"version":        d.Version,
		"key":            d.Key,
		"devices":        len(d.Devices),
		"blocks":         len(d.Blocks),
		"parameter_sets": len(d.ParameterSets),
		"skipped":        len(d.Skipped),
		"loaded_at":      b.deps.Now().UTC().Format(time.RFC3339),
	})
	b.publish(ctx, mqtt.ControllerDesignState, d.CreatedBy)
	b.publish(ctx, mqtt.ControllerDesignAttributes, string(attrs))
}

func (b *Bridge) stateHandler(u *unit) csp.StateHandler {
	return func(connected bool, err error) {
		u.mu.Lock()
		changed := u.connected != connected
		u.connected = connected
		if err != nil {
			u.lastError = err.Error()
		} else {
			u.lastError = ""
		}
		if changed {
			u.since = b.deps.Now()
		}
		u.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
		defer cancel()
		b.publishUnitState(ctx, u)
	}
}
