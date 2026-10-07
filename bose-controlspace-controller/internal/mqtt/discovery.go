package mqtt

import (
	"encoding/json"
	"strconv"

	"github.com/jacobgad/bose-control-space-controller/internal/design"
)

// Origin identifies this add-on in discovery payloads.
type Origin struct {
	Version    string
	SupportURL string
}

// Message is one retained discovery config.
type Message struct {
	Topic   string
	Payload map[string]any
}

// JSON renders the payload.
func (m Message) JSON() string {
	data, _ := json.Marshal(m.Payload)
	return string(data)
}

const (
	manufacturer   = "Bose Professional"
	controllerName = "Bose ControlSpace Controller"

	iconLevel        = "mdi:volume-high"
	iconMute         = "mdi:volume-off"
	iconGain         = "mdi:microphone-settings"
	iconPhantom      = "mdi:flash"
	iconParameterSet = "mdi:playlist-play"
	iconLastRecalled = "mdi:playlist-check"
	iconConnection   = "mdi:lan-connect"
	iconDesign       = "mdi:file-cog"
)

// Entity describes one Home Assistant entity; the common envelope (ids, device,
// origin, availability) is added by Build.
type Entity struct {
	Component string
	// NodeID and Object form the discovery topic and the unique_id.
	NodeID   string
	Object   string
	Name     string
	Category string
	Icon     string
	Device   map[string]any
	// DeviceAvailability, when set, makes the entity unavailable while that unit is unreachable.
	DeviceAvailability string
	Fields             map[string]any
}

// Build renders the discovery message for an entity.
func Build(e Entity, o Origin) Message {
	payload := make(map[string]any, len(e.Fields)+8)
	for k, v := range e.Fields {
		payload[k] = v
	}
	id := NodeID(e.NodeID) + "_" + e.Object
	payload["name"] = e.Name
	payload["unique_id"] = id
	payload["object_id"] = id
	payload["icon"] = e.Icon
	payload["device"] = e.Device
	payload["origin"] = origin(o)
	if e.Category != "" {
		payload["entity_category"] = e.Category
	}
	availability := []map[string]any{controllerAvailability()}
	if e.DeviceAvailability != "" {
		availability = append(availability, map[string]any{"topic": e.DeviceAvailability, "payload_available": PayloadOnline, "payload_not_available": PayloadOffline})
		payload["availability_mode"] = "all"
	}
	payload["availability"] = availability
	return Message{Topic: HADiscoveryTopic(e.Component, NodeID(e.NodeID), e.Object), Payload: payload}
}

func origin(o Origin) map[string]any {
	return map[string]any{"name": controllerName, "sw_version": o.Version, "support_url": o.SupportURL}
}

func controllerAvailability() map[string]any {
	return map[string]any{"topic": ControllerAvailability, "payload_available": PayloadOnline, "payload_not_available": PayloadOffline}
}

// ControllerDevice is the bridge's own device registry entry.
func ControllerDevice(o Origin) map[string]any {
	return map[string]any{
		"identifiers":  []string{ControllerIdentifier},
		"name":         controllerName,
		"manufacturer": "bose-controlspace-controller add-on",
		"model":        "ControlSpace MQTT bridge",
		"sw_version":   o.Version,
	}
}

// PhysicalDevice is the registry entry for an ESP or PowerMatch unit.
func PhysicalDevice(d design.Device) map[string]any {
	device := map[string]any{
		"identifiers":  []string{DeviceIdentifier(d.NodeID)},
		"name":         d.Label,
		"manufacturer": manufacturer,
		"model":        d.Model,
		"via_device":   ControllerIdentifier,
	}
	if d.Firmware != "" {
		device["sw_version"] = d.Firmware
	}
	return device
}

// BlockMessages lists the entities for one block on its device.
func BlockMessages(b design.Block, d design.Device, o Origin) []Message {
	topics := ForBlock(b.NodeID)
	device := PhysicalDevice(d)
	availability := ForDevice(d.NodeID).Availability
	entities := []Entity{
		{
			Component: "number", NodeID: b.NodeID, Object: "level", Name: b.Label + " level", Icon: iconLevel,
			Device: device, DeviceAvailability: availability,
			Fields: map[string]any{
				"state_topic":         topics.LevelState,
				"command_topic":       topics.LevelSet,
				"min":                 b.Level.Min,
				"max":                 b.Level.Max,
				"step":                b.Level.Step,
				"unit_of_measurement": "dB",
				"mode":                "slider",
				"optimistic":          false,
				"retain":              false,
				"qos":                 1,
			},
		},
		{
			Component: "switch", NodeID: b.NodeID, Object: "mute", Name: b.Label + " mute", Icon: iconMute,
			Device: device, DeviceAvailability: availability,
			Fields: map[string]any{
				"state_topic":   topics.MuteState,
				"command_topic": topics.MuteSet,
				"payload_on":    PayloadOn,
				"payload_off":   PayloadOff,
				"optimistic":    false,
				"retain":        false,
				"qos":           1,
			},
		},
	}
	if b.Kind == design.KindInput {
		entities = append(entities,
			Entity{
				Component: "sensor", NodeID: b.NodeID, Object: "gain", Name: b.Label + " preamp gain", Category: "diagnostic", Icon: iconGain,
				Device: device, DeviceAvailability: availability,
				Fields: map[string]any{"state_topic": topics.GainState, "unit_of_measurement": "dB"},
			},
			Entity{
				Component: "binary_sensor", NodeID: b.NodeID, Object: "phantom", Name: b.Label + " phantom power", Category: "diagnostic", Icon: iconPhantom,
				Device: device, DeviceAvailability: availability,
				Fields: map[string]any{"state_topic": topics.PhantomState, "payload_on": PayloadOn, "payload_off": PayloadOff},
			},
		)
	}
	out := make([]Message, 0, len(entities))
	for _, e := range entities {
		out = append(out, Build(e, o))
	}
	return out
}

// ParameterSetMessages lists the recall buttons and the last-recalled sensor on the main device.
func ParameterSetMessages(sets []design.ParameterSet, main design.Device, o Origin) []Message {
	device := PhysicalDevice(main)
	availability := ForDevice(main.NodeID).Availability
	out := make([]Message, 0, len(sets)+1)
	for _, ps := range sets {
		out = append(out, Build(Entity{
			Component: "button", NodeID: ps.NodeID, Object: "recall", Name: "Recall " + ps.Label, Icon: iconParameterSet,
			Device: device, DeviceAvailability: availability,
			Fields: map[string]any{
				"command_topic": ParameterSetPress(ps.ID),
				"payload_press": PayloadPress,
				"retain":        false,
				"qos":           1,
			},
		}, o))
	}
	if len(sets) > 0 {
		topics := ForDevice(main.NodeID)
		out = append(out, Build(Entity{
			Component: "sensor", NodeID: main.NodeID, Object: "last_parameter_set", Name: "Last recalled parameter set", Category: "diagnostic", Icon: iconLastRecalled,
			Device: device, DeviceAvailability: availability,
			Fields: map[string]any{
				"state_topic":           topics.ParameterSetState,
				"json_attributes_topic": topics.ParameterSetAttrs,
			},
		}, o))
	}
	return out
}

// ControllerMessages lists the bridge device's entities.
func ControllerMessages(devices []design.Device, o Origin) []Message {
	out := make([]Message, 0, 1+len(devices))
	out = append(out, Build(Entity{
		Component: "sensor", NodeID: "controller", Object: "design", Name: "Loaded design", Category: "diagnostic", Icon: iconDesign,
		Device: ControllerDevice(o),
		Fields: map[string]any{
			"state_topic":           ControllerDesignState,
			"json_attributes_topic": ControllerDesignAttributes,
		},
	}, o))
	for _, d := range devices {
		topics := ForDevice(d.NodeID)
		out = append(out, Build(Entity{
			Component: "sensor", NodeID: "controller", Object: "connection_" + d.NodeID, Name: d.Label + " connection", Category: "diagnostic", Icon: iconConnection,
			Device: ControllerDevice(o),
			Fields: map[string]any{
				"state_topic":           topics.ConnectionState,
				"json_attributes_topic": topics.ConnectionAttributes,
			},
		}, o))
	}
	return out
}

// DesignMessages is every discovery config for a design.
func DesignMessages(d design.Design, o Origin) []Message {
	var out []Message
	out = append(out, ControllerMessages(d.Devices, o)...)
	for _, b := range d.Blocks {
		dev, ok := d.Device(b.DeviceNodeID)
		if !ok {
			continue
		}
		out = append(out, BlockMessages(b, dev, o)...)
	}
	if main, ok := d.Main(); ok {
		out = append(out, ParameterSetMessages(d.ParameterSets, main, o)...)
	}
	return out
}

// FormatLevelState renders a level for the state topic.
func FormatLevelState(db float64) string {
	return strconv.FormatFloat(db, 'f', -1, 64)
}

// FormatOnOff renders a boolean for ON/OFF state topics.
func FormatOnOff(on bool) string {
	if on {
		return PayloadOn
	}
	return PayloadOff
}
