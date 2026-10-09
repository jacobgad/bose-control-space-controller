// Package design parses ControlSpace Designer project files (.csp) into the devices,
// controllable blocks and parameter sets the bridge exposes.
package design

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html/charset"
)

// DeviceType distinguishes DSP processors from PowerMatch amplifiers; they differ in
// which module indices exist and whether system commands (parameter sets) are accepted.
type DeviceType string

// Device types present in a design.
const (
	DeviceESP        DeviceType = "ESP"
	DevicePowerMatch DeviceType = "PM"
)

// Device is a networked unit that accepts the serial protocol.
type Device struct {
	NodeID   string
	Label    string
	Model    string
	Type     DeviceType
	IP       netip.Addr
	Port     int
	Firmware string
	IsMain   bool
}

// Address is the serial-over-Ethernet endpoint.
func (d Device) Address() string {
	return netip.AddrPortFrom(d.IP, uint16(d.Port)).String() //nolint:gosec // Port is validated to 1..65535 at parse time
}

// BlockKind is a signal-processing module type the bridge knows how to address.
type BlockKind string

// Block kinds exposed to Home Assistant.
const (
	KindGain      BlockKind = "gain"
	KindInput     BlockKind = "input"
	KindAmpOutput BlockKind = "amp_output"
)

// Range is a numeric parameter's bounds as declared by Designer.
type Range struct {
	Min  float64
	Max  float64
	Step float64
}

// Block is one controllable module on a device.
type Block struct {
	NodeID       string
	DeviceNodeID string
	Label        string
	Kind         BlockKind
	Level        Range
}

// ParameterSet is a populated preset. Devices lists the units whose blocks it
// writes, in design order; a recall only needs those units reachable.
type ParameterSet struct {
	ID      int
	NodeID  string
	Label   string
	Devices []string
}

// Design is the parsed project.
type Design struct {
	Version       string
	CreatedBy     string
	Key           string
	Devices       []Device
	Blocks        []Block
	ParameterSets []ParameterSet
	Skipped       []Skipped
}

// Skipped records a block left out of the design and why.
type Skipped struct {
	NodeID string
	Label  string
	Reason string
}

// Device looks a device up by nodeID.
func (d Design) Device(nodeID string) (Device, bool) {
	for _, dev := range d.Devices {
		if dev.NodeID == nodeID {
			return dev, true
		}
	}
	return Device{}, false
}

// WritesTo reports whether any parameter set changes something on the device.
func (d Design) WritesTo(deviceNodeID string) bool {
	for _, ps := range d.ParameterSets {
		if slices.Contains(ps.Devices, deviceNodeID) {
			return true
		}
	}
	return false
}

// BlocksOn lists the blocks hosted by a device, in file order.
func (d Design) BlocksOn(deviceNodeID string) []Block {
	var out []Block
	for _, b := range d.Blocks {
		if b.DeviceNodeID == deviceNodeID {
			out = append(out, b)
		}
	}
	return out
}

// ErrNoDevices is returned for a project with no serial-capable devices.
var ErrNoDevices = errors.New("design: no ESP or PowerMatch devices found")

// LoadFile parses a .csp from disk.
func LoadFile(path string) (Design, error) {
	f, err := os.Open(path) //nolint:gosec // path is the operator's configured design file
	if err != nil {
		return Design{}, err
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

type xmlProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
	Min   string `xml:"min,attr"`
	Max   string `xml:"max,attr"`
	Step  string `xml:"step,attr"`
}

type xmlNode struct {
	ClassName   string        `xml:"className,attr"`
	ESPNodeID   string        `xml:"espNodeID,attr"`
	GonzoNodeID string        `xml:"gonzoNodeID,attr"`
	Properties  []xmlProperty `xml:"Properties>Property"`
}

type xmlParameterSet struct {
	ID         int           `xml:"id,attr"`
	Properties []xmlProperty `xml:"Properties>Property"`
}

type xmlAssign struct {
	TargetType string `xml:"targetType,attr"`
	TargetID   string `xml:"targetID,attr"`
}

type xmlAssignmentSet struct {
	ID      int         `xml:"id,attr"`
	Assigns []xmlAssign `xml:"Assign"`
}

type xmlProject struct {
	Version       string             `xml:"version,attr"`
	CreatedBy     string             `xml:"createdBy,attr"`
	Key           string             `xml:"key,attr"`
	Nodes         []xmlNode          `xml:"Nodes>Node"`
	ParameterSets []xmlParameterSet  `xml:"ParameterSets>ParameterSet"`
	Assignments   []xmlAssignmentSet `xml:"Assignment>ParameterSet"`
}

type properties map[string]xmlProperty

func (p properties) value(name string) string { return p[name].Value }

func (p properties) rangeOf(name string) (Range, error) {
	prop, ok := p[name]
	if !ok {
		return Range{}, fmt.Errorf("missing property %q", name)
	}
	var r Range
	var err error
	if r.Min, err = strconv.ParseFloat(prop.Min, 64); err != nil {
		return Range{}, fmt.Errorf("property %q min: %w", name, err)
	}
	if r.Max, err = strconv.ParseFloat(prop.Max, 64); err != nil {
		return Range{}, fmt.Errorf("property %q max: %w", name, err)
	}
	if r.Step, err = strconv.ParseFloat(prop.Step, 64); err != nil {
		return Range{}, fmt.Errorf("property %q step: %w", name, err)
	}
	return r, nil
}

func (n xmlNode) props() properties {
	out := make(properties, len(n.Properties))
	for _, p := range n.Properties {
		out[p.Name] = p
	}
	return out
}

const (
	classESP        = "Bose.Creator.Nodes.ESP"
	classPowerMatch = "Bose.Creator.Nodes.Gonzo"
	classGain       = "Bose.Creator.Nodes.Gain"
	classInput      = "Bose.Creator.Nodes.Input"
	classOutput     = "Bose.Creator.Nodes.Output"

	defaultSerialPort = 10055
)

// Parse reads a .csp document.
func Parse(r io.Reader) (Design, error) {
	dec := xml.NewDecoder(r)
	dec.CharsetReader = charset.NewReaderLabel
	var project xmlProject
	if err := dec.Decode(&project); err != nil {
		return Design{}, fmt.Errorf("design: parse xml: %w", err)
	}
	d := Design{Version: project.Version, CreatedBy: project.CreatedBy, Key: project.Key}

	for _, n := range project.Nodes {
		switch n.ClassName {
		case classESP, classPowerMatch:
			dev, err := parseDevice(n)
			if err != nil {
				return Design{}, err
			}
			d.Devices = append(d.Devices, dev)
		}
	}
	if len(d.Devices) == 0 {
		return Design{}, ErrNoDevices
	}

	labels := labelIndex(project.Nodes)
	for _, n := range project.Nodes {
		block, ok, err := parseBlock(n, d)
		if err != nil {
			return Design{}, err
		}
		if !ok {
			continue
		}
		if labels[labelKey{block.DeviceNodeID, block.Label}] > 1 {
			d.Skipped = append(d.Skipped, Skipped{NodeID: block.NodeID, Label: block.Label, Reason: "duplicate label on device; serial protocol cannot address it"})
			continue
		}
		if strings.TrimSpace(block.Label) == "" {
			d.Skipped = append(d.Skipped, Skipped{NodeID: block.NodeID, Label: block.Label, Reason: "empty label"})
			continue
		}
		d.Blocks = append(d.Blocks, block)
	}

	d.ParameterSets = parseParameterSets(project, d.Devices)
	return d, nil
}

func parseDevice(n xmlNode) (Device, error) {
	p := n.props()
	dev := Device{
		NodeID:   p.value("nodeID"),
		Label:    p.value("label"),
		Model:    p.value("title"),
		Firmware: p.value("firmwareViersion"),
		IsMain:   strings.EqualFold(p.value("isMainESP"), "True"),
		Port:     defaultSerialPort,
	}
	switch n.ClassName {
	case classESP:
		dev.Type = DeviceESP
	case classPowerMatch:
		dev.Type = DevicePowerMatch
	}
	if dev.NodeID == "" {
		return Device{}, fmt.Errorf("design: device %q has no nodeID", dev.Label)
	}
	ip, err := netip.ParseAddr(p.value("ipAddress"))
	if err != nil {
		return Device{}, fmt.Errorf("design: device %q ipAddress: %w", dev.Label, err)
	}
	dev.IP = ip
	if port := p.value("serialPortNumber"); port != "" {
		v, err := strconv.Atoi(port)
		if err != nil || v < 1 || v > 65535 {
			return Device{}, fmt.Errorf("design: device %q serialPortNumber %q is not a valid port", dev.Label, port)
		}
		dev.Port = v
	}
	return dev, nil
}

func parseBlock(n xmlNode, d Design) (Block, bool, error) {
	var kind BlockKind
	var deviceNodeID string
	switch {
	case n.ClassName == classGain && n.ESPNodeID != "":
		kind, deviceNodeID = KindGain, n.ESPNodeID
	case n.ClassName == classInput && n.ESPNodeID != "":
		kind, deviceNodeID = KindInput, n.ESPNodeID
	case n.ClassName == classOutput && n.GonzoNodeID != "":
		kind, deviceNodeID = KindAmpOutput, n.GonzoNodeID
	default:
		return Block{}, false, nil
	}
	if _, ok := d.Device(deviceNodeID); !ok {
		return Block{}, false, nil
	}
	p := n.props()
	level, err := p.rangeOf("level")
	if err != nil {
		return Block{}, false, fmt.Errorf("design: block %q (%s): %w", p.value("label"), p.value("nodeID"), err)
	}
	return Block{
		NodeID:       p.value("nodeID"),
		DeviceNodeID: deviceNodeID,
		Label:        p.value("label"),
		Kind:         kind,
		Level:        level,
	}, true, nil
}

type labelKey struct{ device, label string }

// labelIndex counts labels per device across every module, not only the exposed
// kinds: the protocol fails for any two modules on one device sharing a name.
func labelIndex(nodes []xmlNode) map[labelKey]int {
	counts := make(map[labelKey]int)
	for _, n := range nodes {
		device := n.ESPNodeID
		if device == "" {
			device = n.GonzoNodeID
		}
		if device == "" {
			continue
		}
		counts[labelKey{device, n.props().value("label")}]++
	}
	return counts
}

func parseParameterSets(project xmlProject, devices []Device) []ParameterSet {
	host := hostIndex(project.Nodes, devices)
	devicesBySet := make(map[int][]string)
	for _, a := range project.Assignments {
		if len(a.Assigns) == 0 {
			continue
		}
		devicesBySet[a.ID] = involvedDevices(a.Assigns, host, devices)
	}
	var out []ParameterSet
	for _, ps := range project.ParameterSets {
		devices, populated := devicesBySet[ps.ID]
		if !populated {
			continue
		}
		p := properties{}
		for _, prop := range ps.Properties {
			p[prop.Name] = prop
		}
		out = append(out, ParameterSet{ID: ps.ID, NodeID: p.value("nodeID"), Label: p.value("label"), Devices: devices})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func hostIndex(nodes []xmlNode, devices []Device) map[string]string {
	out := make(map[string]string, len(nodes))
	for _, dev := range devices {
		out[dev.NodeID] = dev.NodeID
	}
	for _, n := range nodes {
		device := n.ESPNodeID
		if device == "" {
			device = n.GonzoNodeID
		}
		if device == "" {
			continue
		}
		out[n.props().value("nodeID")] = device
	}
	return out
}

func involvedDevices(assigns []xmlAssign, host map[string]string, devices []Device) []string {
	involved := make(map[string]bool)
	for _, a := range assigns {
		if a.TargetType != "Property" {
			continue
		}
		if device, ok := host[a.TargetID]; ok {
			involved[device] = true
		}
	}
	var out []string
	for _, dev := range devices {
		if involved[dev.NodeID] {
			out = append(out, dev.NodeID)
		}
	}
	return out
}
