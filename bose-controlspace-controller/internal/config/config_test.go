package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/config"
)

func TestDefaultsApplyToEmptyOptions(t *testing.T) {
	t.Parallel()
	opts, err := config.ParseOptions([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if opts.PollInterval != 2*time.Second || opts.WriteDebounce != 300*time.Millisecond || opts.LogLevel != slog.LevelInfo {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestExplicitOptionsAreHonoured(t *testing.T) {
	t.Parallel()
	opts, err := config.ParseOptions([]byte(`{"poll_interval_ms":10000,"write_debounce_ms":0,"log_level":"debug"}`))
	if err != nil {
		t.Fatal(err)
	}
	if opts.PollInterval != 10*time.Second || opts.WriteDebounce != 0 || opts.LogLevel != slog.LevelDebug {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestInvalidOptionsAreRejected(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`{"poll_interval_ms":100}`:   "poll_interval_ms",
		`{"write_debounce_ms":9000}`: "write_debounce_ms",
		`{"log_level":"loud"}`:       "log_level",
		`not json`:                   "valid JSON",
	}
	for input, want := range cases {
		_, err := config.ParseOptions([]byte(input))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want mention of %q", input, err, want)
		}
	}
}

func TestMQTTFromEnv(t *testing.T) {
	t.Parallel()
	env := map[string]string{"MQTT_HOST": "broker", "MQTT_PORT": "8883", "MQTT_USERNAME": "u", "MQTT_PASSWORD": "p", "MQTT_SSL": "true"}
	m, err := config.MQTTFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if m.Host != "broker" || m.Port != 8883 || m.Username != "u" || m.Password != "p" || !m.TLS {
		t.Fatalf("mqtt = %+v", m)
	}
	if strings.Contains(m.String(), "p") && strings.Contains(m.String(), "@broker") && strings.Contains(m.String(), ":p") {
		t.Fatal("String must not include the password")
	}
	if _, err := config.MQTTFromEnv(func(string) string { return "" }); err == nil {
		t.Fatal("missing host must fail")
	}
}
