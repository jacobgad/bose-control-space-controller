package mqtt

import (
	"regexp"
	"strconv"
)

// Topic layout and payload constants shared with Home Assistant.
const (
	Prefix            = "bose"
	HADiscoveryPrefix = "homeassistant"
	HAStatusTopic     = HADiscoveryPrefix + "/status"

	PayloadOnline  = "online"
	PayloadOffline = "offline"
	PayloadOn      = "ON"
	PayloadOff     = "OFF"
	PayloadPress   = "PRESS"

	PayloadConnected    = "connected"
	PayloadDisconnected = "disconnected"
	PayloadNone         = "none"

	ControllerNodeID     = Prefix + "_controller"
	ControllerIdentifier = Prefix + ":controller"

	ControllerAvailability     = Prefix + "/controller/availability"
	ControllerDesignState      = Prefix + "/controller/design/state"
	ControllerDesignAttributes = Prefix + "/controller/design/attributes"

	BlockLevelSetWildcard   = Prefix + "/block/+/level/set"
	BlockEnabledSetWildcard = Prefix + "/block/+/enabled/set"
	BlockPhantomSetWildcard = Prefix + "/block/+/phantom/set"
	ParameterSetWildcard    = Prefix + "/parameter_set/+/press"
)

// Parameters a block command topic may name.
const (
	ParameterLevel   = "level"
	ParameterEnabled = "enabled"
	ParameterPhantom = "phantom"
)

// DeviceTopics are the per-physical-unit topics.
type DeviceTopics struct {
	Availability         string
	ConnectionState      string
	ConnectionAttributes string
	ParameterSetState    string
	ParameterSetAttrs    string
}

// ForDevice derives the topic set for a device nodeID.
func ForDevice(nodeID string) DeviceTopics {
	base := Prefix + "/device/" + nodeID
	return DeviceTopics{
		Availability:         base + "/availability",
		ConnectionState:      base + "/connection/state",
		ConnectionAttributes: base + "/connection/attributes",
		ParameterSetState:    base + "/parameter_set/state",
		ParameterSetAttrs:    base + "/parameter_set/attributes",
	}
}

// BlockTopics are the per-module topics.
type BlockTopics struct {
	LevelState   string
	LevelSet     string
	EnabledState string
	EnabledSet   string
	PhantomState string
	PhantomSet   string
}

// ForBlock derives the topic set for a block nodeID.
func ForBlock(nodeID string) BlockTopics {
	base := Prefix + "/block/" + nodeID
	return BlockTopics{
		LevelState:   base + "/level/state",
		LevelSet:     base + "/level/set",
		EnabledState: base + "/enabled/state",
		EnabledSet:   base + "/enabled/set",
		PhantomState: base + "/phantom/state",
		PhantomSet:   base + "/phantom/set",
	}
}

// ParameterSetPress is the command topic for recalling a parameter set.
func ParameterSetPress(id int) string {
	return Prefix + "/parameter_set/" + strconv.Itoa(id) + "/press"
}

// ParameterSetButtonAvailability goes offline when no device the set writes to is reachable.
func ParameterSetButtonAvailability(id int) string {
	return Prefix + "/parameter_set/" + strconv.Itoa(id) + "/availability"
}

// NodeID is the discovery node_id for any nodeID from the design.
func NodeID(nodeID string) string {
	return Prefix + "_" + nodeID
}

// DeviceIdentifier is the Home Assistant device registry identifier for a device.
func DeviceIdentifier(nodeID string) string {
	return Prefix + ":" + nodeID
}

// HADiscoveryTopic builds homeassistant/<component>/<node>/<object>/config.
func HADiscoveryTopic(component, nodeID, objectID string) string {
	return HADiscoveryPrefix + "/" + component + "/" + nodeID + "/" + objectID + "/config"
}

var (
	blockCommandPattern        = regexp.MustCompile(`^` + Prefix + `/block/(\d+)/(` + ParameterLevel + `|` + ParameterEnabled + `|` + ParameterPhantom + `)/set$`)
	parameterSetCommandPattern = regexp.MustCompile(`^` + Prefix + `/parameter_set/(\d+)/press$`)
)

// BlockCommand is a parsed …/block/<nodeID>/<parameter>/set topic.
type BlockCommand struct {
	NodeID    string
	Parameter string
}

// ParseBlockCommand recognises block command topics.
func ParseBlockCommand(topic string) (BlockCommand, bool) {
	m := blockCommandPattern.FindStringSubmatch(topic)
	if m == nil {
		return BlockCommand{}, false
	}
	return BlockCommand{NodeID: m[1], Parameter: m[2]}, true
}

// ParseParameterSetCommand recognises parameter set press topics.
func ParseParameterSetCommand(topic string) (int, bool) {
	m := parameterSetCommandPattern.FindStringSubmatch(topic)
	if m == nil {
		return 0, false
	}
	id, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return id, true
}
