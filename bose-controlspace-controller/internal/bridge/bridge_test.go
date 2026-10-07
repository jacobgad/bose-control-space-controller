package bridge_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/bridge"
	"github.com/jacobgad/bose-control-space-controller/internal/csp"
	mqttpkg "github.com/jacobgad/bose-control-space-controller/internal/mqtt"
)

func TestStartupPublishesAvailabilityDiscoveryAndDesign(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.start(t)

	if h.mqtt.LastPayload(mqttpkg.ControllerAvailability) != "online" {
		t.Fatal("controller availability not online")
	}
	configs := h.mqtt.DiscoveryConfigs()
	const gains, inputs, ampOutputs, buttons, lastRecalled, designSensor, connections = 4, 4, 3, 3, 1, 1, 4
	if want := gains*2 + inputs*4 + ampOutputs*2 + buttons + lastRecalled + designSensor + connections; len(configs) != want {
		t.Fatalf("discovery configs = %d, want %d", len(configs), want)
	}
	level := configs["homeassistant/number/bose_"+hallGain+"/level/config"]
	if level["name"] != "Hall level" || level["max"] != 12.0 || level["min"] != -60.5 || level["command_topic"] != "bose/block/"+hallGain+"/level/set" {
		t.Fatalf("Hall level config = %v", level)
	}
	device := level["device"].(map[string]any)
	if device["name"] != "ESP Main" || device["model"] != "ESP-880" || device["sw_version"] != "v4.100" {
		t.Fatalf("device = %v", device)
	}
	amp := configs["homeassistant/number/bose_"+hallAmp+"/level/config"]
	if amp["max"] != 0.0 {
		t.Fatalf("amp output max = %v", amp["max"])
	}
	if _, ok := configs["homeassistant/button/bose_700002/recall/config"]; !ok {
		t.Fatal("missing Concert recall button")
	}
	if _, ok := configs["homeassistant/sensor/bose_controller/connection_"+espAnnex+"/config"]; !ok {
		t.Fatal("missing ESP Annex connection sensor")
	}
	if h.mqtt.LastPayload(mqttpkg.ControllerDesignState) != "5.14.2.7" {
		t.Fatalf("design state = %q", h.mqtt.LastPayload(mqttpkg.ControllerDesignState))
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(h.mqtt.LastPayload(mqttpkg.ControllerDesignAttributes)), &attrs); err != nil {
		t.Fatal(err)
	}
	if attrs["file"] != "hall-v2.xml" || attrs["blocks"] != 11.0 || attrs["loaded_at"] != "2026-04-07T10:00:00Z" {
		t.Fatalf("design attributes = %v", attrs)
	}
	if !h.logs.Contains("design_loaded") {
		t.Fatal("expected design_loaded log line")
	}
}

func TestPollPublishesDeviceStateOnChangeOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.start(t)

	hall := h.block(hallGain)
	h.waitPayload(t, hall.LevelState, "-2")
	h.waitPayload(t, hall.MuteState, "OFF")
	in := h.block(annexMic)
	h.waitPayload(t, in.LevelState, "0")
	h.waitPayload(t, in.GainState, "44")
	h.waitPayload(t, in.PhantomState, "ON")
	h.waitPayload(t, h.block(hallAmp).LevelState, "-16")
	h.waitPayload(t, mqttpkg.ForDevice(espMain).Availability, "online")
	h.waitPayload(t, mqttpkg.ForDevice(espMain).ConnectionState, "connected")
	h.waitPayload(t, mqttpkg.ForDevice(espMain).ParameterSetState, "none")

	time.Sleep(120 * time.Millisecond)
	if n := len(h.mqtt.MessagesOn(hall.LevelState)); n != 1 {
		t.Fatalf("level republished %d times without changing", n)
	}
	if h.device(espMain).Connections() != 1 {
		t.Fatalf("connections = %d, want one persistent session", h.device(espMain).Connections())
	}
}

func TestOutOfBandChangesAreMirrored(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.start(t)
	hall := h.block(hallGain)
	h.waitPayload(t, hall.LevelState, "-2")

	h.device(espMain).SetModule("Hall", csp.GainLevel, "-7.5")
	h.device(espMain).SetModule("Hall", csp.GainMute, "O")
	h.device(espMain).SetParameterSet(2)

	h.waitPayload(t, hall.LevelState, "-7.5")
	h.waitPayload(t, hall.MuteState, "ON")
	h.waitPayload(t, mqttpkg.ForDevice(espMain).ParameterSetState, "Concert")
	if h.bridge.LastRecalled() != 2 {
		t.Fatalf("last recalled = %d", h.bridge.LastRecalled())
	}
	if countCommands(h.device(espMain).Commands(), "SA") != 0 {
		t.Fatal("polling must never write")
	}
}

func TestLevelCommandIsDebouncedThenConfirmedByReadback(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{debounce: 60 * time.Millisecond})
	h.start(t)
	hall := h.block(hallGain)
	h.waitPayload(t, hall.LevelState, "-2")
	h.device(espMain).ClearCommands()

	for _, v := range []string{"-10", "-9.5", "-9", "-8.5"} {
		h.mqtt.Deliver(hall.LevelSet, v)
		time.Sleep(10 * time.Millisecond)
	}
	h.waitPayload(t, hall.LevelState, "-8.5")

	sets := 0
	for _, c := range h.device(espMain).Commands() {
		if strings.HasPrefix(c, "SA") {
			sets++
			if c != `SA "Hall">1=-8.5` {
				t.Fatalf("unexpected write %q", c)
			}
		}
	}
	if sets != 1 {
		t.Fatalf("writes = %d, want the debounced final value only", sets)
	}
	if !h.logs.Contains("write_confirmed") {
		t.Fatal("expected write_confirmed")
	}
}

func TestMuteCommandIsImmediate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{debounce: 500 * time.Millisecond})
	h.start(t)
	rf := h.block(hallAmp)
	h.waitPayload(t, rf.MuteState, "OFF")

	h.mqtt.Deliver(rf.MuteSet, "ON")
	h.waitPayload(t, rf.MuteState, "ON")
	if v, _ := h.device(ampMain).Module("Hall L R", csp.AmpOutputMute); v != "O" {
		t.Fatalf("device mute = %q", v)
	}
	h.mqtt.Deliver(rf.MuteSet, "OFF")
	h.waitPayload(t, rf.MuteState, "OFF")
}

func TestOutOfRangeLevelIsRejectedWithoutContactingDevice(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.start(t)
	rf := h.block(hallAmp)
	h.waitPayload(t, rf.LevelState, "-16")
	h.device(ampMain).ClearCommands()

	h.mqtt.Deliver(rf.LevelSet, "3")
	h.mqtt.Deliver(rf.LevelSet, "not a number")
	h.mqtt.Deliver("bose/block/999999/level/set", "-3")
	eventually(t, func() bool {
		return h.logs.Contains("level_out_of_range") && h.logs.Contains("level_command_invalid") && h.logs.Contains("command_for_unknown_block")
	}, "rejections logged")
	if countCommands(h.device(ampMain).Commands(), "SA") != 0 {
		t.Fatal("rejected commands must not reach the device")
	}
}

func TestParameterSetRecallGoesToMainDeviceAndUpdatesSensor(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.start(t)
	main := mqttpkg.ForDevice(espMain)
	h.waitPayload(t, main.ParameterSetState, "none")

	h.mqtt.Deliver(mqttpkg.ParameterSetPress(3), "PRESS")
	h.waitPayload(t, main.ParameterSetState, "Rehearsal")
	if h.device(espMain).ParameterSet() != 3 {
		t.Fatalf("device parameter set = %d", h.device(espMain).ParameterSet())
	}
	var attrs map[string]any
	_ = json.Unmarshal([]byte(h.mqtt.LastPayload(main.ParameterSetAttrs)), &attrs)
	if attrs["id"] != 3.0 {
		t.Fatalf("attrs = %v", attrs)
	}
	if !h.logs.Contains("parameter_set_recalled") {
		t.Fatal("expected parameter_set_recalled")
	}

	h.mqtt.Deliver(mqttpkg.ParameterSetPress(9), "PRESS")
	eventually(t, func() bool { return h.logs.Contains("parameter_set_unknown") }, "unknown set rejected")
	if h.device(espAnnex).ParameterSet() != 0 || countCommands(h.device(espAnnex).Commands(), "SS") != 0 {
		t.Fatal("SS must only be sent to the main device")
	}
}

func TestUnreachableDeviceIsOfflineAndOthersKeepWorking(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{unreach: map[string]bool{espAnnex: true}})
	h.start(t)

	j := mqttpkg.ForDevice(espAnnex)
	h.waitPayload(t, j.Availability, "offline")
	h.waitPayload(t, j.ConnectionState, "disconnected")
	eventually(t, func() bool {
		return strings.Contains(h.mqtt.LastPayload(j.ConnectionAttributes), "connection refused")
	}, "last_error attribute")
	h.waitPayload(t, h.block(hallGain).LevelState, "-2")
	h.waitPayload(t, mqttpkg.ForDevice(espMain).Availability, "online")
	if h.mqtt.LastPayload(h.block(annexGain).LevelState) != "" {
		t.Fatal("no state should be published for an unreachable device")
	}
	if n := h.logs.Count("device_disconnected"); n > 1 {
		t.Fatalf("disconnect logged %d times; expected transitions only", n)
	}
}

func TestDeviceDroppingSessionRecoversOnNextPoll(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.start(t)
	hall := h.block(hallGain)
	h.waitPayload(t, hall.LevelState, "-2")

	h.device(espMain).DropConnections()
	availability := mqttpkg.ForDevice(espMain).Availability
	eventually(t, func() bool {
		payloads := h.mqtt.PayloadsOn(availability)
		return len(payloads) >= 3 && payloads[len(payloads)-2] == "offline" && payloads[len(payloads)-1] == "online"
	}, "offline then online after the device hung up")
	h.device(espMain).SetModule("Hall", csp.GainLevel, "-1")
	h.waitPayload(t, hall.LevelState, "-1")
	if h.device(espMain).Connections() != 2 {
		t.Fatalf("connections = %d", h.device(espMain).Connections())
	}
}

func TestRefusedParameterIsLoggedOnceAndSkipped(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.device(espMain).RemoveModule("Hall")
	h.start(t)

	h.waitPayload(t, h.block(foyerGain).LevelState, "-2")
	time.Sleep(100 * time.Millisecond)
	if n := h.logs.Count("parameter_refused"); n != 2 {
		t.Fatalf("parameter_refused logged %d times, want once per parameter (level, mute)", n)
	}
	if h.mqtt.LastPayload(h.block(hallGain).LevelState) != "" {
		t.Fatal("refused parameter must not publish a state")
	}
	h.device(espMain).SetModule("Hall", csp.GainLevel, "-3")
	h.device(espMain).SetModule("Hall", csp.GainMute, "F")
	h.waitPayload(t, h.block(hallGain).LevelState, "-3")
	eventually(t, func() bool { return h.logs.Count("parameter_recovered") == 2 }, "recovery logged")
}

func TestStaleDiscoveryTopicsFromPreviousDesignAreDeleted(t *testing.T) {
	t.Parallel()
	stale := "homeassistant/number/bose_" + retiredGain + "/level/config"
	keep := "homeassistant/number/bose_" + hallGain + "/level/config"
	h := newHarness(t, harnessOptions{manifest: &bridge.MemoryManifest{Topics: []string{stale, keep}}})
	h.start(t)

	msgs := h.mqtt.MessagesOn(stale)
	if len(msgs) != 1 || msgs[0].Payload != "" || !msgs[0].Retain {
		t.Fatalf("stale topic messages = %+v", msgs)
	}
	for _, m := range h.mqtt.MessagesOn(keep) {
		if m.Payload == "" {
			t.Fatal("current topic must not be deleted")
		}
	}
	if !h.logs.Contains("discovery_removed") {
		t.Fatal("expected discovery_removed log")
	}
	if len(h.manifest.Topics) != len(h.mqtt.DiscoveryConfigs()) {
		t.Fatalf("manifest has %d topics, discovery has %d", len(h.manifest.Topics), len(h.mqtt.DiscoveryConfigs()))
	}
}

func TestHomeAssistantRestartReplaysDiscoveryAndState(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.start(t)
	hall := h.block(hallGain)
	h.waitPayload(t, hall.LevelState, "-2")
	h.mqtt.Clear()

	h.mqtt.Deliver(mqttpkg.HAStatusTopic, "online")
	eventually(t, func() bool {
		return len(h.mqtt.MessagesOn("homeassistant/number/bose_"+hallGain+"/level/config")) == 1 &&
			len(h.mqtt.MessagesOn(hall.LevelState)) >= 1 &&
			h.mqtt.LastPayload(mqttpkg.ControllerAvailability) == "online"
	}, "replay after HA restart")
}

func TestStopMarksEverythingOffline(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	if err := h.bridge.Start(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.waitPayload(t, mqttpkg.ForDevice(espMain).Availability, "online")
	h.bridge.Stop(h.ctx)
	if h.mqtt.LastPayload(mqttpkg.ControllerAvailability) != "offline" || h.mqtt.LastPayload(mqttpkg.ForDevice(espMain).Availability) != "offline" {
		t.Fatal("availability not offline after Stop")
	}
	if !h.mqtt.Ended {
		t.Fatal("mqtt session not closed")
	}
}
