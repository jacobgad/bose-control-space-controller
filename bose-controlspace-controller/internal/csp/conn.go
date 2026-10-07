package csp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Transcript receives every byte sequence written to and read from a device.
type Transcript interface {
	Sent(address, command string)
	Received(address string, raw []byte)
}

// Conn is one open serial-over-Ethernet session; it is not safe for concurrent use.
type Conn struct {
	nc         net.Conn
	address    string
	tokens     chan Response
	readErr    error
	readDone   chan struct{}
	transcript Transcript
	closeOnce  sync.Once
}

// ErrClosed is returned once the device has hung up.
var ErrClosed = errors.New("csp: connection closed")

// NewConn wraps an established connection and starts reading responses.
func NewConn(nc net.Conn, address string, transcript Transcript) *Conn {
	c := &Conn{nc: nc, address: address, tokens: make(chan Response, 64), readDone: make(chan struct{}), transcript: transcript}
	go c.readLoop()
	return c
}

// Write sends one command; the terminator is appended.
func (c *Conn) Write(ctx context.Context, command string) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.nc.SetWriteDeadline(deadline)
	} else {
		_ = c.nc.SetWriteDeadline(time.Time{})
	}
	if c.transcript != nil {
		c.transcript.Sent(c.address, command)
	}
	_, err := io.WriteString(c.nc, command+Terminator)
	return err
}

// Next returns the next response, or ctx's error / the read error.
func (c *Conn) Next(ctx context.Context) (Response, error) {
	select {
	case r, ok := <-c.tokens:
		if !ok {
			return Response{}, c.readError()
		}
		return r, nil
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
}

func (c *Conn) readError() error {
	<-c.readDone
	if c.readErr == nil {
		return ErrClosed
	}
	return c.readErr
}

// Close hangs up.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() { err = c.nc.Close() })
	return err
}

// readLoop tokenises the byte stream: ACK is a lone byte, NAK is a byte followed by
// " nn", and everything else is a CR-terminated line; stray CR/LF are dropped.
func (c *Conn) readLoop() {
	defer close(c.readDone)
	defer close(c.tokens)
	br := bufio.NewReader(c.nc)
	var line strings.Builder
	var raw []byte
	inNAK := false
	emit := func(r Response) {
		if c.transcript != nil {
			c.transcript.Received(c.address, raw)
		}
		raw = raw[:0]
		c.tokens <- r
	}
	for {
		b, err := br.ReadByte()
		if err != nil {
			c.readErr = err
			return
		}
		raw = append(raw, b)
		switch {
		case b == ACK && line.Len() == 0 && !inNAK:
			emit(Response{Kind: KindACK})
		case b == NAK && line.Len() == 0:
			inNAK = true
		case b == '\r' || b == '\n':
			switch {
			case inNAK:
				emit(Response{Kind: KindNAK, Raw: line.String(), NAKCode: strings.TrimSpace(line.String())})
			case line.Len() > 0:
				emit(ParseLine(line.String()))
			default:
				raw = raw[:0]
			}
			line.Reset()
			inNAK = false
		default:
			line.WriteByte(b)
			if code := strings.TrimSpace(line.String()); inNAK && len(code) >= 2 {
				emit(Response{Kind: KindNAK, Raw: line.String(), NAKCode: code})
				line.Reset()
				inNAK = false
			}
		}
	}
}
