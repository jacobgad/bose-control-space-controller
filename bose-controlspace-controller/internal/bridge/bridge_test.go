package bridge_test

import (
	"encoding/json"
	"strconv"
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
	const gains, inputs, ampOutputs, buttons, lastRecalled, designSensor, connections = 4, 4, 3, 3, 2, 1, 4
	if want := gains*2 + inputs*3 + ampOutputs*2 + buttons + lastRecalled + designSensor + connections; len(configs) != want {
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
	enabled := configs["homeassistant/switch/bose_"+hallGain+"/enabled/config"]
	if enabled["name"] != "Hall" || enabled["icon"] != "mdi:microphone" || enabled["command_topic"] != "bose/block/"+hallGain+"/enabled/set" {
		t.Fatalf("Hall enabled config = %v", enabled)
	}
	if icon := configs["homeassistant/number/bose_"+hallGain+"/level/config"]["icon"]; icon != "mdi:tune-vertical-variant" {
		t.Fatalf("ESP level icon = %v", icon)
	}
	if icon := configs["homeassistant/switch/bose_"+hallAmp+"/enabled/config"]["icon"]; icon != "mdi:speaker" {
		t.Fatalf("amp output enabled icon = %v", icon)
	}
	if icon := configs["homeassistant/number/bose_"+hallAmp+"/level/config"]["icon"]; icon != "mdi:volume-high" {
		t.Fatalf("amp output level icon = %v", icon)
	}
	phantom := configs["homeassistant/switch/bose_"+annexMic+"/phantom/config"]
	if phantom["entity_category"] != "config" || phantom["command_topic"] != "bose/block/"+annexMic+"/phantom/set" {
		t.Fatalf("phantom config = %v", phantom)
	}
	if _, ok := configs["homeassistant/sensor/bose_"+annexMic+"/gain/config"]; ok {
		t.Fatal("preamp gain must not be exposed")
	}
	recall := configs["homeassistant/button/bose_controller/recall_700003/config"]
	if recall == nil {
		t.Fatal("missing Rehearsal recall button")
	}
	if dev := recall["device"].(map[string]any); dev["identifiers"].([]any)[0] != mqttpkg.ControllerIdentifier {
		t.Fatalf("recall button device = %v", dev)
	}
	if recall["unique_id"] != "bose_controller_recall_700003" {
		t.Fatalf("recall unique_id = %v", recall["unique_id"])
	}
	if topics := availabilityTopics(recall); len(topics) != 2 || topics[1] != mqttpkg.ParameterSetButtonAvailability(3) {
		t.Fatalf("recall button availability = %v", topics)
	}
	last := configs["homeassistant/sensor/bose_"+espAnnex+"/last_parameter_set/config"]
	if last == nil || last["state_topic"] != mqttpkg.ForDevice(espAnnex).ParameterSetState {
		t.Fatalf("annex last recalled sensor = %v", last)
	}
	if _, ok := configs["homeassistant/sensor/bose_"+ampMain+"/last_parameter_set/config"]; ok {
		t.Fatal("units no set writes to must not get a last recalled sensor")
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
	h.waitPayload(t, hall.EnabledState, "ON")
	in := h.block(annexMic)
	h.waitPayload(t, in.LevelState, "0")
	h.waitPayload(t, in.PhantomState, "ON")
	h.waitPayload(t, h.block(hallAmp).LevelState, "-16")
	h.waitPayload(t, mqttpkg.ForDevice(espMain).Availability, "online")
	h.waitPayload(t, mqttpkg.ForDevice(espMain).ConnectionState, "connected")
	h.waitPayload(t, mqttpkg.ForDevice(espMain).ParameterSetState, "none")
	h.waitPayload(t, mqttpkg.ParameterSetButtonAvailability(3), "online")
	if cmds := h.device(ampMain).Commands(); countCommands(cmds, "GS") != 0 {
		t.Fatal("units no set writes to must not be asked for their last recalled set")
	}
	for _, c := range h.device(espAnnex).Commands() {
		if strings.HasPrefix(c, `GA "Annex Mic">`+strconv.Itoa(csp.InputGain)) {
			t.Fatal("preamp gain must not be polled")
		}
	}

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
	h.waitPayload(t, hall.EnabledState, "OFF")
	h.waitPayload(t, mqttpkg.ForDevice(espMain).ParameterSetState, "Concert")
	h.waitPayload(t, mqttpkg.ForDevice(espMain).ParameterSetAttrs, `{"id":2}`)
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

func TestEnabledCommandIsImmediateAndInvertsMute(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{debounce: 500 * time.Millisecond})
	h.start(t)
	rf := h.block(hallAmp)
	h.waitPayload(t, rf.EnabledState, "ON")

	h.mqtt.Deliver(rf.EnabledSet, "OFF")
	h.waitPayload(t, rf.EnabledState, "OFF")
	if v, _ := h.device(ampMain).Module("Hall L R", csp.AmpOutputMute); v != "O" {
		t.Fatalf("device mute = %q, want muted", v)
	}
	h.mqtt.Deliver(rf.EnabledSet, "ON")
	h.waitPayload(t, rf.EnabledState, "ON")
	if v, _ := h.device(ampMain).Module("Hall L R", csp.AmpOutputMute); v != "F" {
		t.Fatalf("device mute = %q, want unmuted", v)
	}
}

func TestPhantomCommandWritesInputIndexFive(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.start(t)
	in := h.block(annexMic)
	h.waitPayload(t, in.PhantomState, "ON")

	h.mqtt.Deliver(in.PhantomSet, "OFF")
	h.waitPayload(t, in.PhantomState, "OFF")
	if v, _ := h.device(espAnnex).Module("Annex Mic", csp.InputPhantom); v != "F" {
		t.Fatalf("device phantom = %q", v)
	}
	h.mqtt.Deliver(h.block(hallGain).PhantomSet, "ON")
	eventually(t, func() bool { return h.logs.Contains("command_for_unknown_parameter") }, "phantom on a gain block rejected")
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

func TestParameterSetRecallReachesOnlyTheUnitsItWritesTo(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.start(t)
	main, annex := mqttpkg.ForDevice(espMain), mqttpkg.ForDevice(espAnnex)
	h.waitPayload(t, main.ParameterSetState, "none")
	h.waitPayload(t, annex.ParameterSetState, "none")

	h.mqtt.Deliver(mqttpkg.ParameterSetPress(3), "PRESS")
	h.waitPayload(t, annex.ParameterSetState, "Rehearsal")
	h.waitPayload(t, annex.ParameterSetAttrs, `{"id":3}`)
	eventually(t, func() bool { return h.logs.Contains("parameter_set_recalled") }, "recall confirmed")
	if h.device(espAnnex).ParameterSet() != 3 || h.device(espMain).ParameterSet() != 0 {
		t.Fatalf("parameter sets: main=%d annex=%d", h.device(espMain).ParameterSet(), h.device(espAnnex).ParameterSet())
	}
	if countCommands(h.device(espMain).Commands(), "SS")+countCommands(h.device(ampMain).Commands(), "SS") != 0 {
		t.Fatal("SS must not reach units the set does not write to")
	}

	h.mqtt.Deliver(mqttpkg.ParameterSetPress(2), "PRESS")
	h.waitPayload(t, main.ParameterSetState, "Concert")
	if h.mqtt.LastPayload(annex.ParameterSetState) != "Rehearsal" {
		t.Fatal("each unit reports its own last recalled set")
	}

	h.mqtt.Deliver(mqttpkg.ParameterSetPress(9), "PRESS")
	eventually(t, func() bool { return h.logs.Contains("parameter_set_unknown") }, "unknown set rejected")
}

func TestParameterSetRecallWorksWhileMainDeviceIsOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{unreach: map[string]bool{espMain: true}})
	h.start(t)
	h.waitPayload(t, mqttpkg.ForDevice(espMain).Availability, "offline")
	h.waitPayload(t, mqttpkg.ParameterSetButtonAvailability(3), "online")
	h.waitPayload(t, mqttpkg.ParameterSetButtonAvailability(2), "offline")

	h.mqtt.Deliver(mqttpkg.ParameterSetPress(3), "PRESS")
	h.waitPayload(t, mqttpkg.ForDevice(espAnnex).ParameterSetState, "Rehearsal")
	if h.device(espAnnex).ParameterSet() != 3 {
		t.Fatalf("annex parameter set = %d", h.device(espAnnex).ParameterSet())
	}
	eventually(t, func() bool { return h.logs.Contains("parameter_set_recalled") }, "recall confirmed")

	h.mqtt.Deliver(mqttpkg.ParameterSetPress(2), "PRESS")
	eventually(t, func() bool { return h.logs.Contains("parameter_set_unreachable") }, "main-only set unreachable")
	if h.mqtt.LastPayload(mqttpkg.ForDevice(espMain).ParameterSetState) != "" {
		t.Fatal("an unreachable unit must not report a last recalled set")
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
	h.waitPayload(t, h.block(hallGain).EnabledState, "ON")
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
