package design_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jacobgad/bose-control-space-controller/internal/design"
)

// Fixtures are synthetic exports for a fictional venue and carry the .xml
// extension because real .csp files are ignored repo-wide.
const (
	currentFixture  = "hall-v2.xml"
	previousFixture = "hall-v1.xml"
)

func load(t *testing.T, name string) design.Design {
	t.Helper()
	d, err := design.LoadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestParsesDevicesFromCurrentDesign(t *testing.T) {
	t.Parallel()
	d := load(t, currentFixture)

	if d.CreatedBy != "5.14.2.7" || d.Version != "5.900" {
		t.Fatalf("header: createdBy=%q version=%q", d.CreatedBy, d.Version)
	}
	want := map[string]design.Device{
		"100001": {Label: "ESP Main", Model: "ESP-880", Type: design.DeviceESP, Firmware: "v4.100", IsMain: true},
		"100002": {Label: "ESP Annex", Model: "ESP-1600", Type: design.DeviceESP, Firmware: "v4.100"},
		"100003": {Label: "Amp Main", Model: "PM8500N", Type: design.DevicePowerMatch, Firmware: "v4.200"},
		"100004": {Label: "Amp Annex", Model: "PM4500N", Type: design.DevicePowerMatch, Firmware: "v4.200"},
	}
	addrs := map[string]string{"100001": "192.0.2.10:10055", "100002": "192.0.2.11:10055", "100003": "192.0.2.20:10055", "100004": "192.0.2.21:10056"}
	if len(d.Devices) != len(want) {
		t.Fatalf("got %d devices, want %d", len(d.Devices), len(want))
	}
	for _, dev := range d.Devices {
		w, ok := want[dev.NodeID]
		if !ok {
			t.Fatalf("unexpected device %+v", dev)
		}
		if dev.Label != w.Label || dev.Model != w.Model || dev.Type != w.Type || dev.Firmware != w.Firmware || dev.IsMain != w.IsMain {
			t.Errorf("device %s = %+v, want %+v", dev.NodeID, dev, w)
		}
		if dev.Address() != addrs[dev.NodeID] {
			t.Errorf("device %s address %s, want %s", dev.NodeID, dev.Address(), addrs[dev.NodeID])
		}
	}
}

func TestExposesOnlyGainInputAndAmpOutputBlocks(t *testing.T) {
	t.Parallel()
	d := load(t, currentFixture)

	counts := map[design.BlockKind]int{}
	byID := map[string]design.Block{}
	for _, b := range d.Blocks {
		counts[b.Kind]++
		byID[b.NodeID] = b
	}
	if counts[design.KindGain] != 4 || counts[design.KindInput] != 4 || counts[design.KindAmpOutput] != 3 {
		t.Fatalf("block counts = %v", counts)
	}

	hall := byID["200001"]
	if hall.Label != "Hall" || hall.DeviceNodeID != "100001" || hall.Kind != design.KindGain {
		t.Errorf("Hall = %+v", hall)
	}
	if hall.Level != (design.Range{Min: -60.5, Max: 12, Step: 0.5}) {
		t.Errorf("Hall level range = %+v", hall.Level)
	}

	mic := byID["300004"]
	if mic.Label != "Annex Mic" || mic.DeviceNodeID != "100002" || mic.Kind != design.KindInput {
		t.Errorf("Annex Mic = %+v", mic)
	}

	amp := byID["500001"]
	if amp.Label != "Hall L R" || amp.DeviceNodeID != "100003" || amp.Kind != design.KindAmpOutput {
		t.Errorf("amp output = %+v", amp)
	}
	if amp.Level.Max != 0 {
		t.Errorf("amp output max = %v, want 0", amp.Level.Max)
	}

	if _, ok := byID["400001"]; ok {
		t.Error("ESP Output block 'To Amp' must not be exposed")
	}
	if _, ok := byID["400002"]; ok {
		t.Error("Mixer block must not be exposed")
	}
	if _, ok := byID["600001"]; ok {
		t.Error("amp Input block must not be exposed")
	}
	if byID["200002"].Label != "Foyer" || byID["500002"].Label != "Foyer" {
		t.Error("the same label on two different devices must be allowed")
	}
	if len(d.Skipped) != 0 {
		t.Errorf("skipped = %+v", d.Skipped)
	}
}

func TestOnlyPopulatedParameterSetsAreExposed(t *testing.T) {
	t.Parallel()
	d := load(t, currentFixture)

	want := []design.ParameterSet{
		{ID: 1, NodeID: "700001", Label: "Service", Devices: []string{"100001"}},
		{ID: 2, NodeID: "700002", Label: "Concert", Devices: []string{"100001"}},
		{ID: 3, NodeID: "700003", Label: "Rehearsal", Devices: []string{"100002"}},
	}
	if len(d.ParameterSets) != len(want) {
		t.Fatalf("parameter sets = %+v", d.ParameterSets)
	}
	for i := range want {
		if !reflect.DeepEqual(d.ParameterSets[i], want[i]) {
			t.Errorf("parameter set %d = %+v, want %+v", i, d.ParameterSets[i], want[i])
		}
	}
}

func TestParameterSetSpanningDevicesListsEachOnce(t *testing.T) {
	t.Parallel()
	d, err := design.Parse(strings.NewReader(`<?xml version="1.0"?><Project version="5.900" createdBy="test"><Nodes>
		<Node className="Bose.Creator.Nodes.ESP"><Properties>
			<Property name="label" value="ESP" /><Property name="nodeID" value="000001" />
			<Property name="ipAddress" value="10.0.0.1" /><Property name="isMainESP" value="True" /></Properties></Node>
		<Node className="Bose.Creator.Nodes.Gonzo"><Properties>
			<Property name="label" value="Amp" /><Property name="nodeID" value="000002" />
			<Property name="ipAddress" value="10.0.0.2" /></Properties></Node>
		<Node className="Bose.Creator.Nodes.Gain" espNodeID="000001"><Properties>
			<Property name="label" value="Hall" /><Property name="nodeID" value="000010" />
			<Property name="level" value="0" min="-60.5" max="12" step="0.5" /></Properties></Node>
		<Node className="Bose.Creator.Nodes.SummingMixer" gonzoNodeID="000002"><Properties>
			<Property name="label" value="Matrix" /><Property name="nodeID" value="000020" /></Properties></Node>
		<Node className="Bose.Creator.Nodes.CC64"><Properties>
			<Property name="label" value="Panel" /><Property name="nodeID" value="000030" /></Properties></Node>
		</Nodes>
		<ParameterSets><ParameterSet id="1"><Properties>
			<Property name="label" value="Both" /><Property name="nodeID" value="000040" /></Properties></ParameterSet></ParameterSets>
		<Assignment><ParameterSet id="1">
			<Assign linkType="SNAPSHOT" targetType="Property" targetID="000020" targetProp="crossPoint1_1" targetValue="True" />
			<Assign linkType="SNAPSHOT" targetType="Property" targetID="000010" targetProp="level" targetValue="-3" />
			<Assign linkType="SNAPSHOT" targetType="Property" targetID="000020" targetProp="crossPoint1_2" targetValue="False" />
			<Assign linkType="SNAPSHOT" targetType="Property" targetID="000030" targetProp="selector" targetValue="1" />
		</ParameterSet></Assignment></Project>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.ParameterSets) != 1 || !reflect.DeepEqual(d.ParameterSets[0].Devices, []string{"000001", "000002"}) {
		t.Fatalf("parameter sets = %+v", d.ParameterSets)
	}
}

func TestParameterSetTargetingDeviceItselfInvolvesThatDevice(t *testing.T) {
	t.Parallel()
	d, err := design.Parse(strings.NewReader(project(``, `
		<ParameterSets><ParameterSet id="1"><Properties>
			<Property name="label" value="Standby" /><Property name="nodeID" value="000040" /></Properties></ParameterSet></ParameterSets>
		<Assignment><ParameterSet id="1">
			<Assign linkType="SNAPSHOT" targetType="Property" targetID="000001" targetProp="standby" targetValue="True" />
		</ParameterSet></Assignment>`)))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.ParameterSets) != 1 || !reflect.DeepEqual(d.ParameterSets[0].Devices, []string{"000001"}) {
		t.Fatalf("parameter sets = %+v", d.ParameterSets)
	}
}

func TestNodeIDsSurviveRenamesAcrossDesignVersions(t *testing.T) {
	t.Parallel()
	old := load(t, previousFixture)
	current := load(t, currentFixture)

	oldByID := map[string]design.Block{}
	for _, b := range old.Blocks {
		oldByID[b.NodeID] = b
	}
	renames := map[string][2]string{
		"200001": {"Main Hall", "Hall"},
		"200004": {"Hall 2", "Annex"},
	}
	for _, b := range current.Blocks {
		if r, ok := renames[b.NodeID]; ok {
			if oldByID[b.NodeID].Label != r[0] || b.Label != r[1] {
				t.Errorf("%s: old=%q new=%q, want %q -> %q", b.NodeID, oldByID[b.NodeID].Label, b.Label, r[0], r[1])
			}
		}
	}
	if _, ok := oldByID["200003"]; ok {
		t.Error("Stage (200003) should not exist in the previous version")
	}
	if _, stillThere := blockByID(current, "200009"); stillThere {
		t.Error("removed Gain 200009 should be absent from the current design")
	}
	if _, wasThere := blockByID(old, "200009"); !wasThere {
		t.Error("Gain 200009 should exist in the previous version")
	}
}

func TestParsesWindows1252EncodedDesign(t *testing.T) {
	t.Parallel()
	utf8, err := os.ReadFile(filepath.Join("testdata", currentFixture))
	if err != nil {
		t.Fatal(err)
	}
	cp1252 := strings.Replace(string(utf8), `encoding="utf-8"`, `encoding="windows-1252"`, 1)
	cp1252 = strings.Replace(cp1252, `value="Foyer"`, "value=\"Caf\xe9\"", 1)

	d, err := design.Parse(strings.NewReader(cp1252))
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := blockByID(d, "200002"); !ok || b.Label != "Café" {
		t.Fatalf("block 200002 = %+v, want label decoded as Café", b)
	}
}

func TestDuplicateLabelsOnOneDeviceAreSkipped(t *testing.T) {
	t.Parallel()
	d, err := design.Parse(strings.NewReader(project(`
		<Node className="Bose.Creator.Nodes.Gain" espNodeID="000001"><Properties>
			<Property name="label" value="Hall" /><Property name="nodeID" value="000010" />
			<Property name="level" value="0" min="-60.5" max="12" step="0.5" /></Properties></Node>
		<Node className="Bose.Creator.Nodes.Gain" espNodeID="000001"><Properties>
			<Property name="label" value="Hall" /><Property name="nodeID" value="000011" />
			<Property name="level" value="0" min="-60.5" max="12" step="0.5" /></Properties></Node>
		<Node className="Bose.Creator.Nodes.Gain" espNodeID="000001"><Properties>
			<Property name="label" value="Foyer" /><Property name="nodeID" value="000012" />
			<Property name="level" value="0" min="-60.5" max="12" step="0.5" /></Properties></Node>`)))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Blocks) != 1 || d.Blocks[0].Label != "Foyer" {
		t.Fatalf("blocks = %+v", d.Blocks)
	}
	if len(d.Skipped) != 2 {
		t.Fatalf("skipped = %+v", d.Skipped)
	}
}

func TestRejectsDesignWithoutDevices(t *testing.T) {
	t.Parallel()
	_, err := design.Parse(strings.NewReader(`<?xml version="1.0"?><Project version="5.900"><Nodes></Nodes></Project>`))
	if err == nil || !strings.Contains(err.Error(), "no ESP or PowerMatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectsDeviceWithInvalidIP(t *testing.T) {
	t.Parallel()
	_, err := design.Parse(strings.NewReader(`<?xml version="1.0"?><Project version="5.900"><Nodes>
		<Node className="Bose.Creator.Nodes.ESP"><Properties>
			<Property name="label" value="ESP" /><Property name="nodeID" value="000001" />
			<Property name="ipAddress" value="not-an-ip" /></Properties></Node></Nodes></Project>`))
	if err == nil || !strings.Contains(err.Error(), "ipAddress") {
		t.Fatalf("err = %v", err)
	}
}

func blockByID(d design.Design, id string) (design.Block, bool) {
	for _, b := range d.Blocks {
		if b.NodeID == id {
			return b, true
		}
	}
	return design.Block{}, false
}

func project(nodes string, sections ...string) string {
	return `<?xml version="1.0"?><Project version="5.900" createdBy="test"><Nodes>
		<Node className="Bose.Creator.Nodes.ESP"><Properties>
			<Property name="label" value="ESP" /><Property name="nodeID" value="000001" />
			<Property name="ipAddress" value="10.0.0.1" /><Property name="isMainESP" value="True" />
			<Property name="serialPortNumber" value="10055" /></Properties></Node>` + nodes + `</Nodes>` + strings.Join(sections, "") + `</Project>`
}
