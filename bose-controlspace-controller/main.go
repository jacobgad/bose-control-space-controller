// Command bose-controlspace-controller is the Home Assistant add-on binary: it bridges
// Bose ControlSpace processors and PowerMatch amplifiers to MQTT so they appear as
// native Home Assistant devices.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/capture"
	"github.com/jacobgad/bose-control-space-controller/internal/config"
	"github.com/jacobgad/bose-control-space-controller/internal/design"
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
	log.Info("starting", "version", version, "mode", cfg.Options.Mode, "design_dir", cfg.DesignDir)

	if cfg.Options.Mode == config.ModeCapture {
		return runCapture(ctx, cfg, log)
	}
	return runBridge(ctx, cfg, log)
}

func runCapture(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	d, file, err := design.Store{Dir: cfg.DesignDir}.Load()
	if err != nil {
		log.Error("design_unavailable", "error", err.Error(), "detail", "upload a .csp on the add-on page in bridge mode first")
		return err
	}
	log.Info("design_loaded", "file", file, "devices", len(d.Devices))
	results, err := capture.Run(ctx, capture.Options{Design: d, Dir: cfg.Options.CaptureDir, Timeout: 3 * time.Second, Log: log})
	if err != nil {
		log.Error("capture_failed", "error", err.Error())
		return err
	}
	fmt.Print(capture.Summarize(results))
	log.Info("capture_finished", "detail", "switch mode back to bridge and restart the add-on")
	<-ctx.Done()
	return nil
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
