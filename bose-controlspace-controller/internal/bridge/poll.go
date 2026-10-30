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
	for _, l := range u.lists {
		if err := b.readListSelection(ctx, u, l); err != nil {
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

// readListSelection returns only transport errors, like the other reads; a unit that
// refuses or garbles the list is logged once and has no reading.
func (b *Bridge) readListSelection(ctx context.Context, u *unit, l *parameterSetList) error {
	raw, err := u.client.Get(ctx, l.Label, csp.ParameterSetListSelection)
	var nak *csp.NAKError
	switch {
	case errors.As(err, &nak):
		b.listRefused(ctx, u, l, err)
		return nil
	case err != nil:
		return err
	}
	position, err := csp.ParsePosition(raw)
	if err != nil {
		l.failOnce(b, u, "parameter_set_list_unparseable", err)
		l.forget(u)
	} else {
		l.recovered(b, u)
		l.record(u, position)
	}
	b.publishListState(ctx, l)
	return nil
}

func (b *Bridge) listRefused(ctx context.Context, u *unit, l *parameterSetList, err error) {
	l.failOnce(b, u, "parameter_set_list_refused", err)
	l.refuse(u)
	b.publishListState(ctx, l)
	b.publishAvailability(ctx, l.topics.Availability, l.reachable())
}

func (l *parameterSetList) failOnce(b *Bridge, u *unit, event string, err error) {
	if l.failed.first(u.NodeID) {
		b.log.Warn(event, "device", u.Label, "label", l.Label, "node_id", l.NodeID, "error", err.Error())
	}
}

func (l *parameterSetList) recovered(b *Bridge, u *unit) {
	if l.failed.cleared(u.NodeID) {
		b.log.Info("parameter_set_list_recovered", "device", u.Label, "label", l.Label)
	}
}

func (bl *block) failOnce(b *Bridge, param, event string, err error) {
	if bl.failed.first(param) {
		b.log.Warn(event, "device", bl.unit.Label, "label", bl.Label, "node_id", bl.NodeID, "parameter", param, "error", err.Error())
	}
}

func (bl *block) recovered(b *Bridge, param string) {
	if bl.failed.cleared(param) {
		b.log.Info("parameter_recovered", "device", bl.unit.Label, "label", bl.Label, "parameter", param)
	}
}
