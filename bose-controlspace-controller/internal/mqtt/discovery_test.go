package mqtt_test

import (
	"reflect"
	"testing"

	"github.com/jacobgad/bose-control-space-controller/internal/design"
	"github.com/jacobgad/bose-control-space-controller/internal/mqtt"
)

func TestListOptionsAreDistinctTrimmedAndNeverReadAsNoSelection(t *testing.T) {
	t.Parallel()
	l := design.ParameterSetList{Members: []design.ListMember{
		{SetID: 1, Label: "Service "},
		{SetID: 2, Label: "Rehearsal"},
		{SetID: 1, Label: "Service"},
		{},
		{SetID: 9},
		{SetID: 3, Label: "None"},
	}}
	want := []string{"Service", "Rehearsal", "Service (3)", "Entry 4", "Parameter set 9", "None (6)"}
	if got := mqtt.ListOptions(l); !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
}
