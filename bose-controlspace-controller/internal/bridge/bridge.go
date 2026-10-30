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
	sets    map[int]*parameterSet
	lists   map[int]*parameterSetList

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

	reportsParameterSet bool
	lists               []*parameterSetList

	mu        sync.Mutex
	connected bool
	lastError string
	since     time.Time
}

func (u *unit) isConnected() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.connected
}

func anyConnected(units []*unit) bool {
	for _, u := range units {
		if u.isConnected() {
			return true
		}
	}
	return false
}

type parameterSet struct {
	design.ParameterSet
	targets []*unit
}

func (ps *parameterSet) reachable() bool { return anyConnected(ps.targets) }

type parameterSetList struct {
	design.ParameterSetList
	topics  mqtt.ParameterSetListTopics
	options []string
	targets []*unit
	failed  failureLog

	mu       sync.Mutex
	readings map[string]int
	refused  map[string]bool
	inFlight int
}

// hosts are the targets not known to refuse the list; a PowerMatch may not keep
// lists even though the sets write to it.
func (l *parameterSetList) hosts() []*unit {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*unit, 0, len(l.targets))
	for _, u := range l.targets {
		if !l.refused[u.NodeID] {
			out = append(out, u)
		}
	}
	return out
}

func (l *parameterSetList) reachable() bool { return anyConnected(l.hosts()) }

func (l *parameterSetList) position(option string) (int, bool) {
	for i, o := range l.options {
		if o == option {
			return i + 1, true
		}
	}
	return 0, false
}

func (l *parameterSetList) record(u *unit, position int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.readings[u.NodeID] = position
	delete(l.refused, u.NodeID)
}

func (l *parameterSetList) refuse(u *unit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.readings, u.NodeID)
	l.refused[u.NodeID] = true
}

func (l *parameterSetList) reading(u *unit) (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	position, ok := l.readings[u.NodeID]
	return position, ok
}

func (l *parameterSetList) forget(u *unit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.readings, u.NodeID)
}

func (l *parameterSetList) beginSelect() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inFlight++
}

func (l *parameterSetList) endSelect() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inFlight--
}

// state is none unless the answering connected targets agree, and withheld
// mid-selection, when units briefly disagree on the way to agreeing.
func (l *parameterSetList) state() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight > 0 {
		return "", false
	}
	seen, agreed := false, 0
	for _, u := range l.targets {
		position, ok := l.readings[u.NodeID]
		if !ok || !u.isConnected() {
			continue
		}
		if seen && position != agreed {
			return mqtt.PayloadNone, true
		}
		seen, agreed = true, position
	}
	if agreed < 1 || agreed > len(l.options) {
		return mqtt.PayloadNone, true
	}
	return l.options[agreed-1], true
}

type block struct {
	design.Block
	unit   *unit
	topics mqtt.BlockTopics
	failed failureLog
}

type failureLog struct {
	mu     sync.Mutex
	failed map[string]bool
}

func (f *failureLog) first(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed[key] {
		return false
	}
	if f.failed == nil {
		f.failed = make(map[string]bool)
	}
	f.failed[key] = true
	return true
}

func (f *failureLog) cleared(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.failed[key] {
		return false
	}
	delete(f.failed, key)
	return true
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
		sets:    make(map[int]*parameterSet),
		lists:   make(map[int]*parameterSetList),
		states:  newStateCache(),
	}
	b.pending = newDebouncer(deps.Options.WriteDebounce)
	for _, d := range deps.Design.Devices {
		u := &unit{Device: d, topics: mqtt.ForDevice(d.NodeID)}
		b.devices[d.NodeID] = u
	}
	for _, blk := range deps.Design.Blocks {
		u, ok := b.devices[blk.DeviceNodeID]
		if !ok {
			continue
		}
		bl := &block{Block: blk, unit: u, topics: mqtt.ForBlock(blk.NodeID)}
		b.blocks[blk.NodeID] = bl
		u.blocks = append(u.blocks, bl)
	}
	for _, ps := range deps.Design.ParameterSets {
		set := &parameterSet{ParameterSet: ps}
		for _, nodeID := range ps.Devices {
			if u, ok := b.devices[nodeID]; ok {
				set.targets = append(set.targets, u)
				u.reportsParameterSet = true
			}
		}
		b.sets[ps.ID] = set
	}
	for _, l := range deps.Design.ParameterSetLists {
		list := &parameterSetList{
			ParameterSetList: l,
			topics:           mqtt.ForParameterSetList(l.ID),
			options:          mqtt.ListOptions(l),
			readings:         make(map[string]int),
			refused:          make(map[string]bool),
		}
		for _, nodeID := range l.Devices {
			if u, ok := b.devices[nodeID]; ok {
				list.targets = append(list.targets, u)
				u.lists = append(u.lists, list)
			}
		}
		b.lists[l.ID] = list
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
		SwitchCommand:       b.SwitchCommand,
		RecallParameterSet:  b.RecallParameterSet,
		SelectListOption:    b.SelectListOption,
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
	b.publishRecallAvailability(ctx)
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
	b.publishRecallAvailability(ctx)
	b.publishAvailability(ctx, mqtt.ControllerAvailability, false)
	if err := b.deps.MQTT.Close(ctx); err != nil {
		b.log.Warn("mqtt_close_failed", "error", err.Error())
	}
}

func (b *Bridge) logDesign() {
	d := b.deps.Design
	b.log.Info("design_loaded", "file", b.deps.DesignFile, "created_by", d.CreatedBy, "devices", len(d.Devices), "blocks", len(d.Blocks), "parameter_sets", len(d.ParameterSets), "parameter_set_lists", len(d.ParameterSetLists))
	for _, s := range d.Skipped {
		b.log.Warn("block_skipped", "node_id", s.NodeID, "label", s.Label, "reason", s.Reason)
	}
	for _, ps := range d.ParameterSets {
		if len(b.sets[ps.ID].targets) == 0 {
			b.log.Warn("parameter_set_no_target_device", "id", ps.ID, "label", ps.Label)
		}
	}
	for _, l := range d.ParameterSetLists {
		if len(b.lists[l.ID].targets) == 0 {
			b.log.Warn("parameter_set_list_no_target_device", "id", l.ID, "label", l.Label)
		}
	}
}

func (b *Bridge) publishDesign(ctx context.Context) {
	d := b.deps.Design
	attrs, _ := json.Marshal(map[string]any{
		"file":                b.deps.DesignFile,
		"created_by":          d.CreatedBy,
		"version":             d.Version,
		"key":                 d.Key,
		"devices":             len(d.Devices),
		"blocks":              len(d.Blocks),
		"parameter_sets":      len(d.ParameterSets),
		"parameter_set_lists": len(d.ParameterSetLists),
		"skipped":             len(d.Skipped),
		"loaded_at":           b.deps.Now().UTC().Format(time.RFC3339),
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
		if !connected {
			for _, l := range u.lists {
				l.forget(u)
				b.publishListState(ctx, l)
			}
		}
		if changed && (u.reportsParameterSet || len(u.lists) > 0) {
			b.publishRecallAvailability(ctx)
		}
	}
}
