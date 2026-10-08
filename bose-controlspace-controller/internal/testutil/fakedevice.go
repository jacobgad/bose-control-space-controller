// Package testutil provides in-memory stand-ins for the broker and the devices.
package testutil

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/jacobgad/bose-control-space-controller/internal/csp"
)

type FakeDevice struct {
	mu           sync.Mutex
	modules      map[string]map[int]string
	parameterSet int
	commands     []string
	conns        []net.Conn
	accepted     int
	silent       bool
	// ACKTerminator is appended after the ACK byte; the protocol leaves this unspecified.
	ACKTerminator string
	// Real ESP/PowerMatch firmware emits ";" here.
	ValueTerminator string
	OnRecall        func(d *FakeDevice, n int)
}

func NewFakeDevice() *FakeDevice {
	return &FakeDevice{modules: make(map[string]map[int]string)}
}

func (d *FakeDevice) SetSilent(silent bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.silent = silent
}

func (d *FakeDevice) SetModule(label string, index int, value string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.modules[label] == nil {
		d.modules[label] = make(map[int]string)
	}
	d.modules[label][index] = value
}

func (d *FakeDevice) Module(label string, index int) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.modules[label][index]
	return v, ok
}

func (d *FakeDevice) RemoveModule(label string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.modules, label)
}

func (d *FakeDevice) SetParameterSet(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.parameterSet = n
}

func (d *FakeDevice) ParameterSet() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.parameterSet
}

func (d *FakeDevice) Commands() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.commands...)
}

func (d *FakeDevice) ClearCommands() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commands = nil
}

func (d *FakeDevice) Connections() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.accepted
}

func (d *FakeDevice) DropConnections() {
	d.mu.Lock()
	conns := d.conns
	d.conns = nil
	d.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (d *FakeDevice) Dial(context.Context, string) (net.Conn, error) {
	client, server := net.Pipe()
	d.mu.Lock()
	d.conns = append(d.conns, server)
	d.accepted++
	d.mu.Unlock()
	go d.serve(server)
	return client, nil
}

var (
	getPattern = regexp.MustCompile(`^GA\s*"([^"]*)"((?:>\d+)+)$`)
	setPattern = regexp.MustCompile(`^SA\s*"([^"]*)"((?:>\d+)+)=(.*)$`)
	ssPattern  = regexp.MustCompile(`^SS\s*([0-9a-fA-F]+)$`)
)

func (d *FakeDevice) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\r')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		d.mu.Lock()
		d.commands = append(d.commands, cmd)
		silent := d.silent
		d.mu.Unlock()
		if silent {
			continue
		}
		if reply := d.handle(cmd); reply != "" {
			if _, err := conn.Write([]byte(reply)); err != nil {
				return
			}
		}
	}
}

func (d *FakeDevice) handle(cmd string) string {
	switch cmd {
	case csp.GetParameterSet:
		return fmt.Sprintf("S %x\r", d.ParameterSet())
	}
	if m := ssPattern.FindStringSubmatch(cmd); m != nil {
		n, _ := strconv.ParseInt(m[1], 16, 32)
		d.SetParameterSet(int(n))
		if d.OnRecall != nil {
			d.OnRecall(d, int(n))
		}
		return ""
	}
	if m := getPattern.FindStringSubmatch(cmd); m != nil {
		label, index := m[1], lastIndex(m[2])
		d.mu.Lock()
		defer d.mu.Unlock()
		module, ok := d.modules[label]
		if !ok {
			return nak("01")
		}
		v, ok := module[index]
		if !ok {
			return nak("02")
		}
		return fmt.Sprintf("GA\"%s\"%s=%s%s\r", label, m[2], v, d.ValueTerminator)
	}
	if m := setPattern.FindStringSubmatch(cmd); m != nil {
		label, index, value := m[1], lastIndex(m[2]), m[3]
		d.mu.Lock()
		defer d.mu.Unlock()
		module, ok := d.modules[label]
		if !ok {
			return nak("01")
		}
		if _, ok := module[index]; !ok {
			return nak("02")
		}
		if value == "T" {
			if module[index] == "O" {
				value = "F"
			} else {
				value = "O"
			}
		}
		module[index] = value
		return string(rune(csp.ACK)) + d.ACKTerminator
	}
	return ""
}

func lastIndex(s string) int {
	parts := strings.Split(strings.TrimPrefix(s, ">"), ">")
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}

func nak(code string) string {
	return string(rune(csp.NAK)) + " " + code + "\r"
}
