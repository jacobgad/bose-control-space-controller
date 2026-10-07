package bridge

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/csp"
	"github.com/jacobgad/bose-control-space-controller/internal/design"
	"github.com/jacobgad/bose-control-space-controller/internal/mqtt"
)

type parameter struct {
	name    string
	index   int
	topic   func(mqtt.BlockTopics) string
	render  func(raw string) (string, error)
	writeOK bool
}

func renderLevel(raw string) (string, error) {
	db, err := csp.ParseLevel(raw)
	if err != nil {
		return "", err
	}
	return mqtt.FormatLevelState(db), nil
}

func renderOnOff(raw string) (string, error) {
	on, err := csp.ParseOnOff(raw)
	if err != nil {
		return "", err
	}
	return mqtt.FormatOnOff(on), nil
}

func renderText(raw string) (string, error) {
	return strings.TrimSpace(raw), nil
}

var parametersByKind = map[design.BlockKind][]parameter{
	design.KindGain: {
		{name: "level", index: csp.GainLevel, topic: func(t mqtt.BlockTopics) string { return t.LevelState }, render: renderLevel, writeOK: true},
		{name: "mute", index: csp.GainMute, topic: func(t mqtt.BlockTopics) string { return t.MuteState }, render: renderOnOff, writeOK: true},
	},
	design.KindInput: {
		{name: "level", index: csp.InputLevel, topic: func(t mqtt.BlockTopics) string { return t.LevelState }, render: renderLevel, writeOK: true},
		{name: "mute", index: csp.InputMute, topic: func(t mqtt.BlockTopics) string { return t.MuteState }, render: renderOnOff, writeOK: true},
		{name: "gain", index: csp.InputGain, topic: func(t mqtt.BlockTopics) string { return t.GainState }, render: renderText},
		{name: "phantom", index: csp.InputPhantom, topic: func(t mqtt.BlockTopics) string { return t.PhantomState }, render: renderOnOff},
	},
	design.KindAmpOutput: {
		{name: "level", index: csp.AmpOutputLevel, topic: func(t mqtt.BlockTopics) string { return t.LevelState }, render: renderLevel, writeOK: true},
		{name: "mute", index: csp.AmpOutputMute, topic: func(t mqtt.BlockTopics) string { return t.MuteState }, render: renderOnOff, writeOK: true},
	},
}

func (bl *block) parameter(name string) (parameter, bool) {
	for _, p := range parametersByKind[bl.Kind] {
		if p.name == name {
			return p, true
		}
	}
	return parameter{}, false
}

func (b *Bridge) pollLoop(u *unit) {
	defer b.wg.Done()
	ticker := time.NewTicker(b.deps.Options.PollInterval)
	defer ticker.Stop()
	for {
		b.pollUnit(u)
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pollUnit reads every parameter on a device once. A transport failure ends the
// cycle early (the client has already dropped the session); a NAK only skips that
// parameter.
func (b *Bridge) pollUnit(u *unit) {
	ctx := b.ctx
	for _, bl := range u.blocks {
		for _, p := range parametersByKind[bl.Kind] {
			if err := b.readParameter(ctx, bl, p); err != nil {
				var nak *csp.NAKError
				if errors.As(err, &nak) {
					continue
				}
				return
			}
		}
	}
	if u == b.main && len(b.sets) > 0 {
		id, err := u.client.LastParameterSet(ctx)
		if err != nil {
			return
		}
		b.publishLastRecalled(ctx, id)
	}
}

func (b *Bridge) readParameter(ctx context.Context, bl *block, p parameter) error {
	raw, err := bl.unit.client.Get(ctx, bl.Label, p.index)
	if err != nil {
		var nak *csp.NAKError
		if errors.As(err, &nak) {
			bl.failOnce(b, p.name, "parameter_refused", err)
		}
		return err
	}
	rendered, err := p.render(raw)
	if err != nil {
		bl.failOnce(b, p.name, "parameter_unparseable", err)
		return nil
	}
	bl.recovered(b, p.name)
	b.publish(ctx, p.topic(bl.topics), rendered)
	return nil
}

func (bl *block) failOnce(b *Bridge, param, event string, err error) {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	if bl.failed[param] {
		return
	}
	bl.failed[param] = true
	b.log.Warn(event, "device", bl.unit.Label, "label", bl.Label, "node_id", bl.NodeID, "parameter", param, "error", err.Error())
}

func (bl *block) recovered(b *Bridge, param string) {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	if bl.failed[param] {
		delete(bl.failed, param)
		b.log.Info("parameter_recovered", "device", bl.unit.Label, "label", bl.Label, "parameter", param)
	}
}
