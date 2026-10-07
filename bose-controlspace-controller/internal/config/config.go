// Package config loads add-on options from /data/options.json, the MQTT broker
// details from either the environment or the Home Assistant Supervisor, and the
// fixed paths the add-on owns under /data.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Mode selects what the binary does at startup.
type Mode string

// Modes.
const (
	ModeBridge  Mode = "bridge"
	ModeCapture Mode = "capture"
)

// Options are the validated add-on options.
type Options struct {
	PollInterval  time.Duration
	WriteDebounce time.Duration
	Mode          Mode
	CaptureDir    string
	LogLevel      slog.Level
}

// MQTT is how to reach the broker.
type MQTT struct {
	Host     string
	Port     int
	Username string
	Password string
	TLS      bool
}

// String renders the broker address without the password, so formatting an MQTT
// value (or any struct containing one) can never leak the credential into logs.
func (m MQTT) String() string {
	return fmt.Sprintf("mqtt://%s@%s:%d tls=%v", m.Username, m.Host, m.Port, m.TLS)
}

// GoString mirrors String for %#v, which bypasses Stringer.
func (m MQTT) GoString() string { return m.String() }

// LogValue renders the broker details for slog without the password.
func (m MQTT) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", m.Host),
		slog.Int("port", m.Port),
		slog.String("username", m.Username),
		slog.Bool("tls", m.TLS),
	)
}

// Config is everything the binary needs to start.
type Config struct {
	Options      Options
	MQTT         MQTT
	ManifestPath string
	// DesignDir holds the uploaded project file; it lives in /data so it survives
	// restarts and is included in add-on backups.
	DesignDir   string
	WebAddr     string
	IngressOnly bool
}

type rawOptions struct {
	PollIntervalMs  *int    `json:"poll_interval_ms"`
	WriteDebounceMs *int    `json:"write_debounce_ms"`
	Mode            *string `json:"mode"`
	CaptureDir      *string `json:"capture_dir"`
	LogLevel        *string `json:"log_level"`
}

// Defaults applied when an option is absent.
const (
	DefaultCaptureDir    = "/share/bose/capture"
	DefaultPollInterval  = 2000 * time.Millisecond
	DefaultWriteDebounce = 300 * time.Millisecond
)

// ParseOptions validates the JSON contents of options.json and applies defaults.
func ParseOptions(data []byte) (Options, error) {
	var raw rawOptions
	if err := json.Unmarshal(data, &raw); err != nil {
		return Options{}, fmt.Errorf("options are not valid JSON: %w", err)
	}
	opts := Options{
		PollInterval:  DefaultPollInterval,
		WriteDebounce: DefaultWriteDebounce,
		Mode:          ModeBridge,
		CaptureDir:    DefaultCaptureDir,
		LogLevel:      slog.LevelInfo,
	}
	if raw.PollIntervalMs != nil {
		if v := *raw.PollIntervalMs; v < 500 || v > 600000 {
			return Options{}, errors.New("poll_interval_ms must be between 500 and 600000")
		}
		opts.PollInterval = time.Duration(*raw.PollIntervalMs) * time.Millisecond
	}
	if raw.WriteDebounceMs != nil {
		if v := *raw.WriteDebounceMs; v < 0 || v > 5000 {
			return Options{}, errors.New("write_debounce_ms must be between 0 and 5000")
		}
		opts.WriteDebounce = time.Duration(*raw.WriteDebounceMs) * time.Millisecond
	}
	if raw.Mode != nil {
		switch Mode(*raw.Mode) {
		case ModeBridge, ModeCapture:
			opts.Mode = Mode(*raw.Mode)
		default:
			return Options{}, errors.New("mode must be bridge or capture")
		}
	}
	if raw.CaptureDir != nil && strings.TrimSpace(*raw.CaptureDir) != "" {
		opts.CaptureDir = strings.TrimSpace(*raw.CaptureDir)
	}
	if raw.LogLevel != nil {
		if err := opts.LogLevel.UnmarshalText([]byte(*raw.LogLevel)); err != nil {
			return Options{}, errors.New("log_level must be one of debug, info, warn, error")
		}
	}
	return opts, nil
}

// MQTTFromEnv reads MQTT_HOST, MQTT_PORT, MQTT_USERNAME, MQTT_PASSWORD and MQTT_SSL.
func MQTTFromEnv(getenv func(string) string) (MQTT, error) {
	host := getenv("MQTT_HOST")
	if host == "" {
		return MQTT{}, errors.New("MQTT_HOST is required")
	}
	m := MQTT{Host: host, Port: 1883, Username: getenv("MQTT_USERNAME"), Password: getenv("MQTT_PASSWORD")}
	if p := getenv("MQTT_PORT"); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return MQTT{}, fmt.Errorf("MQTT_PORT %q is not a valid port", p)
		}
		m.Port = port
	}
	switch strings.ToLower(getenv("MQTT_SSL")) {
	case "true", "1", "yes":
		m.TLS = true
	}
	return m, nil
}

const supervisorServicesURL = "http://supervisor/services/mqtt"

// MQTTFromSupervisor fetches the broker registered with the Supervisor services API,
// which is reachable without hassio_api once the add-on declares the mqtt service.
func MQTTFromSupervisor(ctx context.Context, token string, client *http.Client) (MQTT, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, supervisorServicesURL, nil)
	if err != nil {
		return MQTT{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return MQTT{}, fmt.Errorf("supervisor request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return MQTT{}, fmt.Errorf("supervisor returned HTTP %d for the MQTT service", resp.StatusCode)
	}
	var payload struct {
		Result string `json:"result"`
		Data   struct {
			Host     string `json:"host"`
			Port     int    `json:"port"`
			Username string `json:"username"`
			Password string `json:"password"`
			SSL      bool   `json:"ssl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Result != "ok" || payload.Data.Host == "" {
		return MQTT{}, errors.New("supervisor did not return a usable MQTT service; is the Mosquitto broker add-on running?")
	}
	return MQTT{Host: payload.Data.Host, Port: payload.Data.Port, Username: payload.Data.Username, Password: payload.Data.Password, TLS: payload.Data.SSL}, nil
}

// Load reads options.json and resolves MQTT settings, preferring MQTT_HOST when set.
// Capture mode needs no broker, so MQTT resolution is skipped for it.
func Load(ctx context.Context) (Config, error) {
	optionsPath := envOr("BOSE_OPTIONS_PATH", "/data/options.json")
	data, err := os.ReadFile(optionsPath) //nolint:gosec // path is fixed by the add-on or set by the operator's own environment
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", optionsPath, err)
	}
	opts, err := ParseOptions(data)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Options:      opts,
		ManifestPath: envOr("BOSE_MANIFEST_PATH", "/data/discovery-manifest.json"),
		DesignDir:    envOr("BOSE_DESIGN_DIR", "/data/design"),
		WebAddr:      envOr("BOSE_WEB_ADDR", ":8099"),
		IngressOnly:  os.Getenv("SUPERVISOR_TOKEN") != "",
	}
	if opts.Mode == ModeCapture {
		return cfg, nil
	}
	switch {
	case os.Getenv("MQTT_HOST") != "":
		cfg.MQTT, err = MQTTFromEnv(os.Getenv)
	case os.Getenv("SUPERVISOR_TOKEN") != "":
		cfg.MQTT, err = MQTTFromSupervisor(ctx, os.Getenv("SUPERVISOR_TOKEN"), &http.Client{Timeout: 10 * time.Second})
	default:
		err = errors.New("no MQTT configuration: set MQTT_HOST or run under the Home Assistant Supervisor")
	}
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
