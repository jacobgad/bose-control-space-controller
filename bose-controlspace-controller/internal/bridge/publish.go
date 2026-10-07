package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/mqtt"
)

// stateCache remembers the last payload per topic so unchanged values are not
// republished, and so everything can be replayed when Home Assistant restarts.
type stateCache struct {
	mu     sync.Mutex
	values map[string]string
}

func newStateCache() *stateCache {
	return &stateCache{values: make(map[string]string)}
}

func (c *stateCache) set(topic, payload string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.values[topic]; ok && old == payload {
		return false
	}
	c.values[topic] = payload
	return true
}

func (c *stateCache) snapshot() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

func (b *Bridge) publish(ctx context.Context, topic, payload string) {
	if !b.states.set(topic, payload) {
		return
	}
	b.send(ctx, topic, payload)
}

func (b *Bridge) send(ctx context.Context, topic, payload string) {
	if err := b.deps.MQTT.Publish(ctx, topic, payload, true); err != nil && !errors.Is(err, mqtt.ErrNotConnected) {
		b.log.Warn("mqtt_publish_failed", "topic", topic, "error", err.Error())
	}
}

func (b *Bridge) publishAvailability(ctx context.Context, topic string, online bool) {
	payload := mqtt.PayloadOffline
	if online {
		payload = mqtt.PayloadOnline
	}
	b.publish(ctx, topic, payload)
}

func (b *Bridge) publishUnitState(ctx context.Context, u *unit) {
	u.mu.Lock()
	connected, lastError, since := u.connected, u.lastError, u.since
	u.mu.Unlock()
	state := mqtt.PayloadDisconnected
	if connected {
		state = mqtt.PayloadConnected
	}
	attrs := map[string]any{"ip": u.IP.String(), "port": u.Port, "node_id": u.NodeID, "last_error": lastError}
	if !since.IsZero() {
		attrs["since"] = since.UTC().Format(time.RFC3339)
	}
	data, _ := json.Marshal(attrs)
	b.publishAvailability(ctx, u.topics.Availability, connected)
	b.publish(ctx, u.topics.ConnectionState, state)
	b.publish(ctx, u.topics.ConnectionAttributes, string(data))
}

func (b *Bridge) publishLastRecalled(ctx context.Context, id int) {
	if b.main == nil {
		return
	}
	b.mu.Lock()
	b.lastRecalled = id
	b.mu.Unlock()
	label := mqtt.PayloadNone
	if ps, ok := b.sets[id]; ok {
		label = ps.Label
	} else if id != 0 {
		label = "Parameter set " + strconv.Itoa(id)
	}
	attrs, _ := json.Marshal(map[string]any{"id": id})
	b.publish(ctx, b.main.topics.ParameterSetState, label)
	b.publish(ctx, b.main.topics.ParameterSetAttrs, string(attrs))
}

func (b *Bridge) publishDiscovery(ctx context.Context) error {
	messages := mqtt.DesignMessages(b.deps.Design, b.deps.Origin)
	current := make(map[string]bool, len(messages))
	topics := make([]string, 0, len(messages))
	for _, m := range messages {
		current[m.Topic] = true
		topics = append(topics, m.Topic)
	}
	previous, err := b.deps.Manifest.Load()
	if err != nil {
		b.log.Warn("manifest_unreadable", "error", err.Error())
	}
	sort.Strings(previous)
	for _, topic := range previous {
		if !current[topic] {
			b.log.Info("discovery_removed", "topic", topic)
			b.send(ctx, topic, "")
		}
	}
	for _, m := range messages {
		b.send(ctx, m.Topic, m.JSON())
	}
	if err := b.deps.Manifest.Save(topics); err != nil {
		b.log.Warn("manifest_save_failed", "error", err.Error())
	}
	b.log.Info("discovery_published", "entities", len(messages), "removed", len(previous)-countShared(previous, current))
	return nil
}

func countShared(previous []string, current map[string]bool) int {
	n := 0
	for _, t := range previous {
		if current[t] {
			n++
		}
	}
	return n
}

func (b *Bridge) republish() {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 30*time.Second)
	defer cancel()
	for _, m := range mqtt.DesignMessages(b.deps.Design, b.deps.Origin) {
		b.send(ctx, m.Topic, m.JSON())
	}
	for topic, payload := range b.states.snapshot() {
		b.send(ctx, topic, payload)
	}
	b.send(ctx, mqtt.ControllerAvailability, mqtt.PayloadOnline)
}
