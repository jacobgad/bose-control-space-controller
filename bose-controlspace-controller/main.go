// Command bose-controlspace-controller is the Home Assistant add-on binary: it bridges
// Bose ControlSpace processors and PowerMatch amplifiers to MQTT so they appear as
// native Home Assistant devices.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/config"
	"github.com/jacobgad/bose-control-space-controller/internal/webui"
)

var version = "dev"

const (
	supportURL      = "https://github.com/jacobgad/bose-control-space-controller"
	shutdownTimeout = 10 * time.Second
	deviceTimeout   = 2 * time.Second
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := config.Load(ctx)
	if err != nil {
		newLogger(slog.LevelInfo).Error("config_invalid", "detail", err.Error())
		return err
	}
	log := newLogger(cfg.Options.LogLevel)
	log.Info("starting", "version", version, "design_dir", cfg.DesignDir)
	return runBridge(ctx, cfg, log)
}

func runBridge(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	a := newApp(ctx, cfg, log)
	if err := a.start(); err != nil {
		return err
	}
	defer a.stop()

	deps := webui.Deps{Status: a.currentStatus, Upload: a.upload, Log: log}
	if cfg.IngressOnly {
		deps.AllowedRemote = webui.IngressProxy
	}
	if err := webui.Serve(ctx, cfg.WebAddr, webui.Handler(deps), log); err != nil {
		log.Error("webui_failed", "error", err.Error())
		return err
	}
	log.Info("shutdown_requested")
	return nil
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
