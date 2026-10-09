package bridge

import (
	"context"
	"errors"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/csp"
	"github.com/jacobgad/bose-control-space-controller/internal/design"
	"github.com/jacobgad/bose-control-space-controller/internal/mqtt"
)

type parameter struct {
	name     string
	index    int
	topic    func(mqtt.BlockTopics) string
	render   func(raw string) (string, error)
	inverted bool
}

func (p parameter) encodeSwitch(on bool) string {
	return csp.FormatOnOff(on != p.inverted)
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

func renderInverted(raw string) (string, error) {
	on, err := csp.ParseOnOff(raw)
	if err != nil {
		return "", err
	}
	return mqtt.FormatOnOff(!on), nil
}

func levelParameter(index int) parameter {
	return parameter{name: mqtt.ParameterLevel, index: index, topic: func(t mqtt.BlockTopics) string { return t.LevelState }, render: renderLevel}
}

func enabledParameter(muteIndex int) parameter {
	return parameter{name: mqtt.ParameterEnabled, index: muteIndex, topic: func(t mqtt.BlockTopics) string { return t.EnabledState }, render: renderInverted, inverted: true}
}

var parametersByKind = map[design.BlockKind][]parameter{
	design.KindGain: {levelParameter(csp.GainLevel), enabledParameter(csp.GainMute)},
	design.KindInput: {
		levelParameter(csp.InputLevel),
		enabledParameter(csp.InputMute),
		{name: mqtt.ParameterPhantom, index: csp.InputPhantom, topic: func(t mqtt.BlockTopics) string { return t.PhantomState }, render: renderOnOff},
	},
	design.KindAmpOutput: {levelParameter(csp.AmpOutputLevel), enabledParameter(csp.AmpOutputMute)},
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
	if u.reportsParameterSet {
		if _, err := b.readLastRecalled(ctx, u); err != nil {
			return
		}
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

func (b *Bridge) readLastRecalled(ctx context.Context, u *unit) (int, error) {
	id, err := u.client.LastParameterSet(ctx)
	if err != nil {
		return 0, err
	}
	b.publishLastRecalled(ctx, u, id)
	return id, nil
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
