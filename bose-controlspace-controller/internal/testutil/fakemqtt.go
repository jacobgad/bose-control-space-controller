package testutil

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/mqtt"
)

type Published struct {
	Topic   string
	Payload string
	Retain  bool
}

type FakeMQTT struct {
	// Delay is applied to every Publish to give concurrent publishers a chance to interleave.
	Delay         time.Duration
	mu            sync.Mutex
	published     []Published
	subscriptions []string
	onMessage     []mqtt.MessageHandler
	onConnect     []func()
	connected     bool
	Ended         bool
}

func NewFakeMQTT(connected bool) *FakeMQTT {
	return &FakeMQTT{connected: connected}
}

func (f *FakeMQTT) Publish(_ context.Context, topic, payload string, retain bool) error {
	if f.Delay > 0 {
		time.Sleep(f.Delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, Published{Topic: topic, Payload: payload, Retain: retain})
	return nil
}

func (f *FakeMQTT) Subscribe(_ context.Context, topics []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscriptions = append(f.subscriptions, topics...)
	return nil
}

func (f *FakeMQTT) OnMessage(h mqtt.MessageHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onMessage = append(f.onMessage, h)
}

func (f *FakeMQTT) OnConnect(h func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onConnect = append(f.onConnect, h)
}

func (f *FakeMQTT) AwaitConnection(ctx context.Context) error {
	if f.Connected() {
		return nil
	}
	return ctx.Err()
}

func (f *FakeMQTT) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *FakeMQTT) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Ended = true
	f.connected = false
	return nil
}

func (f *FakeMQTT) Deliver(topic, payload string) {
	f.mu.Lock()
	handlers := append([]mqtt.MessageHandler{}, f.onMessage...)
	f.mu.Unlock()
	for _, h := range handlers {
		h(topic, []byte(payload))
	}
}

func (f *FakeMQTT) SimulateDisconnect() {
	f.mu.Lock()
	f.connected = false
	f.mu.Unlock()
}

func (f *FakeMQTT) SimulateConnect() {
	f.mu.Lock()
	f.connected = true
	handlers := append([]func(){}, f.onConnect...)
	f.mu.Unlock()
	for _, h := range handlers {
		h()
	}
}

func (f *FakeMQTT) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = nil
}

func (f *FakeMQTT) Published() []Published {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Published(nil), f.published...)
}

func (f *FakeMQTT) Subscriptions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.subscriptions...)
}

func (f *FakeMQTT) MessagesOn(topic string) []Published {
	var out []Published
	for _, p := range f.Published() {
		if p.Topic == topic {
			out = append(out, p)
		}
	}
	return out
}

func (f *FakeMQTT) PayloadsOn(topic string) []string {
	msgs := f.MessagesOn(topic)
	out := make([]string, 0, len(msgs))
	for _, p := range msgs {
		out = append(out, p.Payload)
	}
	return out
}

func (f *FakeMQTT) LastOn(topic string) (Published, bool) {
	msgs := f.MessagesOn(topic)
	if len(msgs) == 0 {
		return Published{}, false
	}
	return msgs[len(msgs)-1], true
}

func (f *FakeMQTT) LastPayload(topic string) string {
	last, _ := f.LastOn(topic)
	return last.Payload
}

func (f *FakeMQTT) Retained() map[string]string {
	out := make(map[string]string)
	for _, p := range f.Published() {
		if !p.Retain {
			continue
		}
		if p.Payload == "" {
			delete(out, p.Topic)
		} else {
			out[p.Topic] = p.Payload
		}
	}
	return out
}

func (f *FakeMQTT) DiscoveryConfigs() map[string]map[string]any {
	out := make(map[string]map[string]any)
	for topic, payload := range f.Retained() {
		if strings.HasPrefix(topic, "homeassistant/") && strings.HasSuffix(topic, "/config") {
			var cfg map[string]any
			if json.Unmarshal([]byte(payload), &cfg) == nil {
				out[topic] = cfg
			}
		}
	}
	return out
}
