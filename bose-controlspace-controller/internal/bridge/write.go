package bridge

import (
	"context"
	"sync"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/csp"
)

type debouncer struct {
	delay   time.Duration
	mu      sync.Mutex
	timers  map[string]*time.Timer
	stopped bool
}

func newDebouncer(delay time.Duration) *debouncer {
	return &debouncer{delay: delay, timers: make(map[string]*time.Timer)}
}

func (d *debouncer) schedule(key string, fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	if d.delay == 0 {
		go fn()
		return
	}
	if t, ok := d.timers[key]; ok {
		t.Stop()
	}
	d.timers[key] = time.AfterFunc(d.delay, func() {
		d.mu.Lock()
		delete(d.timers, key)
		d.mu.Unlock()
		fn()
	})
}

func (d *debouncer) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	for key, t := range d.timers {
		t.Stop()
		delete(d.timers, key)
	}
}

// LevelCommand sets a block's level after the debounce window, then reads it back.
func (b *Bridge) LevelCommand(nodeID string, db float64) {
	bl, ok := b.blocks[nodeID]
	if !ok {
		b.log.Warn("command_for_unknown_block", "node_id", nodeID, "parameter", "level")
		return
	}
	if db < bl.Level.Min || db > bl.Level.Max {
		b.log.Warn("level_out_of_range", "label", bl.Label, "node_id", nodeID, "value", db, "min", bl.Level.Min, "max", bl.Level.Max)
		return
	}
	b.pending.schedule(nodeID+"/level", func() {
		b.writeConfirmed(bl, "level", csp.FormatLevel(db))
	})
}

// MuteCommand sets a block's mute immediately, then reads it back.
func (b *Bridge) MuteCommand(nodeID string, muted bool) {
	bl, ok := b.blocks[nodeID]
	if !ok {
		b.log.Warn("command_for_unknown_block", "node_id", nodeID, "parameter", "mute")
		return
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.writeConfirmed(bl, "mute", csp.FormatOnOff(muted))
	}()
}

func (b *Bridge) writeConfirmed(bl *block, name, value string) {
	if b.ctx.Err() != nil {
		return
	}
	p, ok := bl.parameter(name)
	if !ok || !p.writeOK {
		return
	}
	ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
	defer cancel()
	log := b.log.With("device", bl.unit.Label, "label", bl.Label, "node_id", bl.NodeID, "parameter", name, "value", value)
	if err := bl.unit.client.Set(ctx, bl.Label, value, p.index); err != nil {
		log.Warn("write_failed", "error", err.Error())
		return
	}
	if err := b.readParameter(ctx, bl, p); err != nil {
		log.Warn("readback_failed", "error", err.Error())
		return
	}
	log.Info("write_confirmed")
}

// RecallParameterSet sends SS n to the main device and refreshes the last-recalled sensor.
func (b *Bridge) RecallParameterSet(id int) {
	ps, ok := b.sets[id]
	if !ok {
		b.log.Warn("parameter_set_unknown", "id", id)
		return
	}
	if b.main == nil {
		b.log.Warn("parameter_set_no_main_device", "id", id)
		return
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if b.ctx.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
		defer cancel()
		log := b.log.With("id", id, "label", ps.Label)
		if err := b.main.client.RecallParameterSet(ctx, id); err != nil {
			log.Warn("parameter_set_recall_failed", "error", err.Error())
			return
		}
		last, err := b.main.client.LastParameterSet(ctx)
		if err != nil {
			log.Warn("parameter_set_readback_failed", "error", err.Error())
			return
		}
		b.publishLastRecalled(ctx, last)
		if last != id {
			log.Warn("parameter_set_not_recalled", "reported", last)
			return
		}
		log.Info("parameter_set_recalled")
	}()
}

// LastRecalled is the parameter set id most recently reported by the main device.
func (b *Bridge) LastRecalled() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastRecalled
}
