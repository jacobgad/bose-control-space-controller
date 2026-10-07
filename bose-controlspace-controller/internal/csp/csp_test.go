package csp_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/csp"
	"github.com/jacobgad/bose-control-space-controller/internal/testutil"
)

func TestCommandFormatting(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		csp.GetModule("Hall", csp.GainLevel):                                   `GA "Hall">1`,
		csp.SetModule("Hall", "-12.5", csp.GainLevel):                          `SA "Hall">1=-12.5`,
		csp.SetModule("Mic 1", csp.OnValue, csp.InputMute):                     `SA "Mic 1">4=O`,
		csp.GetModule("Delay 1", 1, 1):                                         `GA "Delay 1">1>1`,
		csp.RecallParameterSet(11):                                             "SS b",
		csp.RecallParameterSet(3):                                              "SS 3",
		csp.FormatLevel(-60.5):                                                 "-60.5",
		csp.FormatLevel(0):                                                     "0",
		csp.FormatLevel(3):                                                     "3",
		csp.SetModule("RF LF RM LM", csp.FormatLevel(-16), csp.AmpOutputLevel): `SA "RF LF RM LM">1=-16`,
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestParseLineRecognisesModuleValuesAndParameterSets(t *testing.T) {
	t.Parallel()
	r := csp.ParseLine(`GA"Gain 4">1=3`)
	if !r.Matches("Gain 4", []int{1}) || r.Value != "3" {
		t.Fatalf("module value = %+v", r)
	}
	r = csp.ParseLine(`GA "Delay 2">1>1>=9600`)
	if !r.Matches("Delay 2", []int{1, 1}) || r.Value != "9600" {
		t.Fatalf("two-index value = %+v", r)
	}
	r = csp.ParseLine("S b")
	if r.Kind != csp.KindParameterSet || r.ParameterSet != 11 {
		t.Fatalf("parameter set = %+v", r)
	}
	r = csp.ParseLine("SUB yes")
	if r.Kind != csp.KindLine || r.Raw != "SUB yes" {
		t.Fatalf("plain line = %+v", r)
	}
}

func TestValueCodecs(t *testing.T) {
	t.Parallel()
	if v, err := csp.ParseLevel("-6"); err != nil || v != -6 {
		t.Fatalf("ParseLevel = %v, %v", v, err)
	}
	if on, err := csp.ParseOnOff("O"); err != nil || !on {
		t.Fatalf("ParseOnOff O = %v, %v", on, err)
	}
	if on, err := csp.ParseOnOff("F"); err != nil || on {
		t.Fatalf("ParseOnOff F = %v, %v", on, err)
	}
	if _, err := csp.ParseOnOff("T"); err == nil {
		t.Fatal("T must not parse as a state")
	}
}

func newClient(t *testing.T, dev *testutil.FakeDevice) *csp.Client {
	t.Helper()
	c := csp.NewClient(csp.ClientOptions{Address: "192.0.2.10:10055", Dial: dev.Dial, Timeout: 200 * time.Millisecond})
	t.Cleanup(c.Close)
	return c
}

func TestGetAndSetRoundTrip(t *testing.T) {
	t.Parallel()
	dev := testutil.NewFakeDevice()
	dev.SetModule("Hall", csp.GainLevel, "-2.0")
	dev.SetModule("Hall", csp.GainMute, "F")
	c := newClient(t, dev)
	ctx := t.Context()

	if v, err := c.Get(ctx, "Hall", csp.GainLevel); err != nil || v != "-2.0" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if err := c.Set(ctx, "Hall", "-12.5", csp.GainLevel); err != nil {
		t.Fatal(err)
	}
	if v, _ := dev.Module("Hall", csp.GainLevel); v != "-12.5" {
		t.Fatalf("device level = %q", v)
	}
	if err := c.Set(ctx, "Hall", csp.OnValue, csp.GainMute); err != nil {
		t.Fatal(err)
	}
	if v, err := c.Get(ctx, "Hall", csp.GainMute); err != nil || v != "O" {
		t.Fatalf("mute = %q, %v", v, err)
	}
	if dev.Connections() != 1 {
		t.Fatalf("connections = %d, want one persistent session", dev.Connections())
	}
}

func TestACKWithTrailingCarriageReturnIsAccepted(t *testing.T) {
	t.Parallel()
	dev := testutil.NewFakeDevice()
	dev.ACKTerminator = "\r"
	dev.SetModule("Hall", csp.AmpOutputLevel, "-3")
	c := newClient(t, dev)
	if err := c.Set(t.Context(), "Hall", "-6", csp.AmpOutputLevel); err != nil {
		t.Fatal(err)
	}
	if v, err := c.Get(t.Context(), "Hall", csp.AmpOutputLevel); err != nil || v != "-6" {
		t.Fatalf("after ACK+CR, Get = %q, %v", v, err)
	}
}

func TestNAKIsReportedWithCode(t *testing.T) {
	t.Parallel()
	dev := testutil.NewFakeDevice()
	dev.SetModule("Hall", csp.AmpOutputLevel, "-3")
	c := newClient(t, dev)

	_, err := c.Get(t.Context(), "Nope", 1)
	var nak *csp.NAKError
	if !errors.As(err, &nak) || nak.Code != "01" {
		t.Fatalf("unknown module err = %v", err)
	}
	err = c.Set(t.Context(), "Hall", "1", 9)
	if !errors.As(err, &nak) || nak.Code != "02" {
		t.Fatalf("bad index err = %v", err)
	}
	if !c.Connected() {
		t.Fatal("a NAK must not drop the session")
	}
}

func TestParameterSetRecallAndQuery(t *testing.T) {
	t.Parallel()
	dev := testutil.NewFakeDevice()
	c := newClient(t, dev)
	ctx := t.Context()
	if n, err := c.LastParameterSet(ctx); err != nil || n != 0 {
		t.Fatalf("initial GS = %d, %v", n, err)
	}
	if err := c.RecallParameterSet(ctx, 11); err != nil {
		t.Fatal(err)
	}
	if n, err := c.LastParameterSet(ctx); err != nil || n != 11 {
		t.Fatalf("GS after SS = %d, %v", n, err)
	}
	cmds := dev.Commands()
	if cmds[1] != "SS b" {
		t.Fatalf("commands = %v", cmds)
	}
}

func TestSilentDeviceTimesOutAndRedialsNextCall(t *testing.T) {
	t.Parallel()
	dev := testutil.NewFakeDevice()
	dev.SetSilent(true)
	dev.SetModule("Hall", 1, "0")
	var mu sync.Mutex
	var states []bool
	c := csp.NewClient(csp.ClientOptions{Address: "x", Dial: dev.Dial, Timeout: 50 * time.Millisecond, OnState: func(connected bool, _ error) {
		mu.Lock()
		defer mu.Unlock()
		states = append(states, connected)
	}})
	t.Cleanup(c.Close)

	_, err := c.Get(t.Context(), "Hall", 1)
	if err == nil || !strings.Contains(err.Error(), "no response") {
		t.Fatalf("err = %v", err)
	}
	if c.Connected() {
		t.Fatal("timeout must drop the session")
	}
	dev.SetSilent(false)
	if v, err := c.Get(t.Context(), "Hall", 1); err != nil || v != "0" {
		t.Fatalf("after redial Get = %q, %v", v, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(states) != 3 || !states[0] || states[1] || !states[2] {
		t.Fatalf("state transitions = %v", states)
	}
}

func TestDeviceHangingUpIsSurfacedAndRecovered(t *testing.T) {
	t.Parallel()
	dev := testutil.NewFakeDevice()
	dev.SetModule("Hall", 1, "0")
	c := newClient(t, dev)
	if _, err := c.Get(t.Context(), "Hall", 1); err != nil {
		t.Fatal(err)
	}
	dev.DropConnections()
	_, err := c.Get(t.Context(), "Hall", 1)
	if err == nil {
		t.Fatal("expected an error after the device hung up")
	}
	if v, err := c.Get(t.Context(), "Hall", 1); err != nil || v != "0" {
		t.Fatalf("recovery Get = %q, %v", v, err)
	}
	if dev.Connections() != 2 {
		t.Fatalf("connections = %d", dev.Connections())
	}
}

func TestDialFailureIsReported(t *testing.T) {
	t.Parallel()
	c := csp.NewClient(csp.ClientOptions{Address: "x", Timeout: 50 * time.Millisecond, Dial: func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}})
	_, err := c.Get(t.Context(), "Hall", 1)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnsolicitedLinesAreSkippedWhileWaiting(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go func() {
		buf := make([]byte, 64)
		_, _ = server.Read(buf)
		_, _ = server.Write([]byte("GA\"#Hall\">2=O\r" + "GA\"Foyer\">1=-4\r"))
	}()
	c := csp.NewClient(csp.ClientOptions{Address: "x", Timeout: 200 * time.Millisecond, Dial: func(context.Context, string) (net.Conn, error) { return client, nil }})
	if v, err := c.Get(t.Context(), "Foyer", 1); err != nil || v != "-4" {
		t.Fatalf("Get = %q, %v", v, err)
	}
}
