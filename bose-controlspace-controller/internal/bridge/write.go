package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// SwitchCommand sets an on/off parameter immediately, then reads it back.
func (b *Bridge) SwitchCommand(nodeID, name string, on bool) {
	bl, ok := b.blocks[nodeID]
	if !ok {
		b.log.Warn("command_for_unknown_block", "node_id", nodeID, "parameter", name)
		return
	}
	p, ok := bl.parameter(name)
	if !ok {
		b.log.Warn("command_for_unknown_parameter", "node_id", nodeID, "parameter", name)
		return
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.writeConfirmed(bl, name, p.encodeSwitch(on))
	}()
}

func (b *Bridge) writeConfirmed(bl *block, name, value string) {
	if b.ctx.Err() != nil {
		return
	}
	p, ok := bl.parameter(name)
	if !ok {
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

// RecallParameterSet sends SS n to every unit the set writes to, so the set lands
// on whichever of them are powered.
func (b *Bridge) RecallParameterSet(id int) {
	set, ok := b.sets[id]
	if !ok {
		b.log.Warn("parameter_set_unknown", "id", id)
		return
	}
	if len(set.targets) == 0 {
		b.log.Warn("parameter_set_no_target_device", "id", id, "label", set.Label)
		return
	}
	b.fanOut(b.log.With("id", id, "label", set.Label), set.targets, fanOutEvents{
		failed: "parameter_set_recall_failed", unreachable: "parameter_set_unreachable", partial: "parameter_set_partial", done: "parameter_set_recalled",
	}, func(ctx context.Context, u *unit) error { return b.recallOn(ctx, u, id) }, nil)
}

// SelectListOption selects a member of a Parameter Set List on every unit its sets
// write to, like a recall.
func (b *Bridge) SelectListOption(id int, option string) {
	l, ok := b.lists[id]
	if !ok {
		b.log.Warn("parameter_set_list_unknown", "id", id)
		return
	}
	position, ok := l.position(option)
	if !ok {
		b.log.Warn("parameter_set_list_option_unknown", "id", id, "label", l.Label, "option", option)
		return
	}
	if len(l.targets) == 0 {
		b.log.Warn("parameter_set_list_no_target_device", "id", id, "label", l.Label)
		return
	}
	log := b.log.With("id", id, "label", l.Label, "option", option, "position", position)
	hosts := l.hosts()
	if len(hosts) == 0 {
		log.Warn("parameter_set_list_unhosted")
		return
	}
	l.beginSelect()
	b.fanOut(log, hosts, fanOutEvents{
		failed: "parameter_set_list_select_failed", unreachable: "parameter_set_list_unreachable", partial: "parameter_set_list_partial", done: "parameter_set_list_selected",
	}, func(ctx context.Context, u *unit) error { return b.selectOn(ctx, u, l, position) }, func(ctx context.Context) {
		l.endSelect()
		b.publishListState(ctx, l)
	})
}

type fanOutEvents struct {
	failed, unreachable, partial, done string
}

// finish runs even when the bridge is stopping, so callers can release state there.
func (b *Bridge) fanOut(log *slog.Logger, targets []*unit, events fanOutEvents, apply func(context.Context, *unit) error, finish func(context.Context)) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
		defer cancel()
		if finish != nil {
			defer finish(ctx)
		}
		if b.ctx.Err() != nil {
			return
		}

		results := make([]error, len(targets))
		var wg sync.WaitGroup
		for i, u := range targets {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = apply(ctx, u)
			}()
		}
		wg.Wait()

		var reached, missed []string
		for i, u := range targets {
			if results[i] != nil {
				log.Warn(events.failed, "device", u.Label, "error", results[i].Error())
				missed = append(missed, u.Label)
				continue
			}
			reached = append(reached, u.Label)
		}
		switch {
		case len(reached) == 0:
			log.Warn(events.unreachable, "devices", missed)
		case len(missed) > 0:
			log.Warn(events.partial, "reached", reached, "missed", missed)
		default:
			log.Info(events.done, "devices", reached)
		}
	}()
}

func (b *Bridge) selectOn(ctx context.Context, u *unit, l *parameterSetList, position int) error {
	if err := u.client.Set(ctx, l.Label, csp.FormatPosition(position), csp.ParameterSetListSelect); err != nil {
		var nak *csp.NAKError
		if errors.As(err, &nak) {
			b.listRefused(ctx, u, l, err)
		}
		return err
	}
	if err := b.readListSelection(ctx, u, l); err != nil {
		return fmt.Errorf("readback: %w", err)
	}
	got, ok := l.reading(u)
	if !ok {
		return errors.New("readback refused or unparseable")
	}
	if got != position {
		return fmt.Errorf("device reports position %d", got)
	}
	return nil
}

func (b *Bridge) recallOn(ctx context.Context, u *unit, id int) error {
	if err := u.client.RecallParameterSet(ctx, id); err != nil {
		return err
	}
	last, err := b.readLastRecalled(ctx, u)
	if err != nil {
		return fmt.Errorf("readback: %w", err)
	}
	if last != id {
		return fmt.Errorf("device reports set %d", last)
	}
	return nil
}
