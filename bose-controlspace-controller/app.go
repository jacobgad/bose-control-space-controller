package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/bridge"
	"github.com/jacobgad/bose-control-space-controller/internal/config"
	"github.com/jacobgad/bose-control-space-controller/internal/design"
	"github.com/jacobgad/bose-control-space-controller/internal/mqtt"
	"github.com/jacobgad/bose-control-space-controller/internal/webui"
)

type app struct {
	ctx   context.Context
	cfg   config.Config
	store design.Store
	log   *slog.Logger

	mu     sync.Mutex
	bridge *bridge.Bridge
	status webui.Status
}

func newApp(ctx context.Context, cfg config.Config, log *slog.Logger) *app {
	return &app{ctx: ctx, cfg: cfg, store: design.Store{Dir: cfg.DesignDir}, log: log}
}

// A missing or unparsable design is not fatal because the page exists to fix it;
// an unreachable broker is.
func (a *app) start() error {
	d, file, err := a.store.Load()
	if err != nil {
		a.log.Warn("design_unavailable", "error", err.Error(), "detail", "open the add-on page and upload a .csp")
		a.mu.Lock()
		a.status = webui.Status{Err: err}
		a.mu.Unlock()
		return nil
	}
	return a.swap(d, file)
}

func (a *app) upload(name string, data []byte) error {
	d, file, err := a.store.Save(name, data)
	if err != nil {
		return err
	}
	return a.swap(d, file)
}

func (a *app) swap(d design.Design, file string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bridge != nil {
		a.log.Info("design_replacing", "file", file)
		shutdown(a.bridge)
		a.bridge = nil
	}
	b, err := a.startBridge(d, file)
	if err != nil {
		a.status = webui.Status{Err: err}
		return err
	}
	a.bridge = b
	a.status = webui.Status{Design: &d, File: file, LoadedAt: time.Now()}
	return nil
}

func (a *app) startBridge(d design.Design, file string) (*bridge.Bridge, error) {
	conn, err := mqtt.Connect(a.ctx, mqtt.PahoOptions{
		Settings: a.cfg.MQTT,
		ClientID: fmt.Sprintf("bose-controlspace-controller-%d", os.Getpid()),
		Will:     mqtt.Will{Topic: mqtt.ControllerAvailability, Payload: mqtt.PayloadOffline},
		Log:      a.log,
	})
	if err != nil {
		a.log.Error("mqtt_setup_failed", "error", err.Error())
		return nil, err
	}
	b := bridge.New(bridge.Deps{
		Design:        d,
		DesignFile:    file,
		MQTT:          conn,
		Options:       a.cfg.Options,
		Log:           a.log,
		Origin:        mqtt.Origin{Version: version, SupportURL: supportURL},
		Manifest:      bridge.FileManifest{Path: a.cfg.ManifestPath},
		DeviceTimeout: deviceTimeout,
	})
	if err := b.Start(a.ctx); err != nil {
		a.log.Error("startup_failed", "error", err.Error())
		shutdown(b)
		return nil, err
	}
	return b, nil
}

func (a *app) currentStatus() webui.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

func (a *app) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bridge != nil {
		shutdown(a.bridge)
		a.bridge = nil
	}
}

func shutdown(b *bridge.Bridge) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	b.Stop(ctx)
}
