// Package csp speaks the Bose ControlSpace Serial Control Protocol (v5.13) over
// serial-over-Ethernet: command formatting, response parsing and a reconnecting
// per-device client.
package csp

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Control bytes used by module commands.
const (
	ACK = 0x06
	NAK = 0x15
)

// Terminator ends every command.
const Terminator = "\r"

// Module parameter indices, per the protocol's module tables.
const (
	GainLevel = 1
	GainMute  = 2

	InputGain    = 2
	InputLevel   = 3
	InputMute    = 4
	InputPhantom = 5

	AmpOutputLevel = 1
	AmpOutputMute  = 2
)

// GetModule formats GA "Label">i1>i2.
func GetModule(label string, indices ...int) string {
	return "GA " + moduleRef(label, indices)
}

// SetModule formats SA "Label">i1>i2=value.
func SetModule(label, value string, indices ...int) string {
	return "SA " + moduleRef(label, indices) + "=" + value
}

func moduleRef(label string, indices []int) string {
	var b strings.Builder
	b.WriteByte('"')
	b.WriteString(label)
	b.WriteByte('"')
	for _, i := range indices {
		b.WriteByte('>')
		b.WriteString(strconv.Itoa(i))
	}
	return b.String()
}

// RecallParameterSet formats SS n; n is sent in hexadecimal as system commands require.
func RecallParameterSet(n int) string {
	return fmt.Sprintf("SS %x", n)
}

// System and device commands without arguments.
const (
	GetParameterSet      = "GS"
	ProbeSubscription    = "SUB"
	GetAmpConfiguration  = "GC"
	GetAmpStandby        = "GY"
	GetAmpFaultStatus    = "GF"
	OnValue              = "O"
	OffValue             = "F"
	MinusInfinityLevelDB = -60.5
)

// FormatLevel renders a dB value as the protocol expects: plain ASCII, no units.
func FormatLevel(db float64) string {
	return strconv.FormatFloat(db, 'f', -1, 64)
}

// ParseLevel reads a dB value from a response.
func ParseLevel(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("csp: level %q: %w", s, err)
	}
	return v, nil
}

// FormatOnOff renders a boolean as O/F.
func FormatOnOff(on bool) string {
	if on {
		return OnValue
	}
	return OffValue
}

// ParseOnOff reads an O/F response value.
func ParseOnOff(s string) (bool, error) {
	switch strings.TrimSpace(s) {
	case OnValue:
		return true, nil
	case OffValue:
		return false, nil
	}
	return false, fmt.Errorf("csp: expected O or F, got %q", s)
}

// Kind classifies a response token.
type Kind int

// Response kinds.
const (
	KindLine Kind = iota
	KindACK
	KindNAK
	KindModuleValue
	KindParameterSet
)

// Response is one parsed unit from the device.
type Response struct {
	Kind Kind
	// Raw is the text as received, without the terminator (empty for ACK).
	Raw          string
	NAKCode      string
	Label        string
	Indices      []int
	Value        string
	ParameterSet int
}

// NAKError is a module command the device refused.
type NAKError struct {
	Command string
	Code    string
}

func (e *NAKError) Error() string {
	return fmt.Sprintf("csp: %s refused: NAK %s (%s)", e.Command, e.Code, nakReason(e.Code))
}

func nakReason(code string) string {
	switch code {
	case "01":
		return "invalid module name"
	case "02":
		return "illegal index"
	case "03":
		return "value out of range"
	}
	return "unknown error"
}

var (
	modulePattern       = regexp.MustCompile(`^GA\s*"([^"]*)"((?:>\d+)*)>?=(.*)$`)
	parameterSetPattern = regexp.MustCompile(`^S\s*([0-9a-fA-F]+)$`)
)

// ParseLine classifies a text line from the device.
func ParseLine(line string) Response {
	line = strings.TrimRight(line, "\r\n")
	if m := modulePattern.FindStringSubmatch(line); m != nil {
		r := Response{Kind: KindModuleValue, Raw: line, Label: m[1], Value: m[3]}
		for _, idx := range strings.Split(strings.TrimPrefix(m[2], ">"), ">") {
			if idx == "" {
				continue
			}
			n, _ := strconv.Atoi(idx)
			r.Indices = append(r.Indices, n)
		}
		return r
	}
	if m := parameterSetPattern.FindStringSubmatch(line); m != nil {
		n, _ := strconv.ParseInt(m[1], 16, 32)
		return Response{Kind: KindParameterSet, Raw: line, ParameterSet: int(n)}
	}
	return Response{Kind: KindLine, Raw: line}
}

// Matches reports whether a module value response answers a GA for label/indices.
func (r Response) Matches(label string, indices []int) bool {
	if r.Kind != KindModuleValue || r.Label != label || len(r.Indices) != len(indices) {
		return false
	}
	for i := range indices {
		if r.Indices[i] != indices[i] {
			return false
		}
	}
	return true
}
