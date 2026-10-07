package csp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Dialer opens the TCP session; swapped out in tests.
type Dialer func(ctx context.Context, address string) (net.Conn, error)

// NetDialer dials with the given timeout.
func NetDialer(timeout time.Duration) Dialer {
	return func(ctx context.Context, address string) (net.Conn, error) {
		d := net.Dialer{Timeout: timeout}
		return d.DialContext(ctx, "tcp", address)
	}
}

// StateHandler is told when the session comes up or goes down.
type StateHandler func(connected bool, err error)

// ClientOptions configure a Client.
type ClientOptions struct {
	Address    string
	Dial       Dialer
	Timeout    time.Duration
	Log        *slog.Logger
	Transcript Transcript
	OnState    StateHandler
}

// Client is a serialised, lazily reconnecting session to one device. Any transport
// error or timeout drops the session; the next call redials.
type Client struct {
	opts ClientOptions
	mu   sync.Mutex
	conn *Conn
}

// NewClient creates a disconnected client.
func NewClient(opts ClientOptions) *Client {
	if opts.Dial == nil {
		opts.Dial = NetDialer(opts.Timeout)
	}
	if opts.Timeout == 0 {
		opts.Timeout = 2 * time.Second
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Client{opts: opts}
}

func (c *Client) ensureLocked(ctx context.Context) error {
	if c.conn != nil {
		return nil
	}
	dialCtx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	nc, err := c.opts.Dial(dialCtx, c.opts.Address)
	if err != nil {
		c.notify(false, err)
		return fmt.Errorf("csp: connect %s: %w", c.opts.Address, err)
	}
	c.conn = NewConn(nc, c.opts.Address, c.opts.Transcript)
	c.opts.Log.Info("device_connected", "address", c.opts.Address)
	c.notify(true, nil)
	return nil
}

func (c *Client) dropLocked(err error) {
	if c.conn == nil {
		return
	}
	_ = c.conn.Close()
	c.conn = nil
	c.opts.Log.Warn("device_disconnected", "address", c.opts.Address, "error", err.Error())
	c.notify(false, err)
}

func (c *Client) notify(connected bool, err error) {
	if c.opts.OnState != nil {
		c.opts.OnState(connected, err)
	}
}

// Close hangs up without redialing.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
		c.notify(false, ErrClosed)
	}
}

// Get queries a module parameter and returns its raw value.
func (c *Client) Get(ctx context.Context, label string, indices ...int) (string, error) {
	cmd := GetModule(label, indices...)
	r, err := c.request(ctx, cmd, func(r Response) bool { return r.Kind == KindNAK || r.Matches(label, indices) })
	if err != nil {
		return "", err
	}
	if r.Kind == KindNAK {
		return "", &NAKError{Command: cmd, Code: r.NAKCode}
	}
	return r.Value, nil
}

// Set writes a module parameter and waits for the device to accept it.
func (c *Client) Set(ctx context.Context, label, value string, indices ...int) error {
	cmd := SetModule(label, value, indices...)
	r, err := c.request(ctx, cmd, func(r Response) bool { return r.Kind == KindACK || r.Kind == KindNAK })
	if err != nil {
		return err
	}
	if r.Kind == KindNAK {
		return &NAKError{Command: cmd, Code: r.NAKCode}
	}
	return nil
}

// LastParameterSet returns the number of the most recently recalled parameter set
// (0 if none since power-up).
func (c *Client) LastParameterSet(ctx context.Context) (int, error) {
	r, err := c.request(ctx, GetParameterSet, func(r Response) bool { return r.Kind == KindParameterSet })
	if err != nil {
		return 0, err
	}
	return r.ParameterSet, nil
}

// RecallParameterSet sends SS n. System commands are not acknowledged, so success is
// only observable through a following LastParameterSet.
func (c *Client) RecallParameterSet(ctx context.Context, n int) error {
	return c.send(ctx, RecallParameterSet(n))
}

func (c *Client) send(ctx context.Context, command string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureLocked(ctx); err != nil {
		return err
	}
	if err := c.conn.Write(ctx, command); err != nil {
		c.dropLocked(err)
		return err
	}
	c.opts.Log.Debug("device_sent", "command", command)
	return nil
}

// request writes a command and waits for the first response accepted by want.
// Other responses in the meantime (unsolicited notifications) are logged and skipped.
func (c *Client) request(ctx context.Context, command string, want func(Response) bool) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureLocked(ctx); err != nil {
		return Response{}, err
	}
	if err := c.conn.Write(ctx, command); err != nil {
		c.dropLocked(err)
		return Response{}, err
	}
	c.opts.Log.Debug("device_sent", "command", command)
	waitCtx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	for {
		r, err := c.conn.Next(waitCtx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				err = fmt.Errorf("csp: %s: no response within %s", command, c.opts.Timeout)
			}
			c.dropLocked(err)
			return Response{}, err
		}
		c.opts.Log.Debug("device_received", "command", command, "response", r.Raw)
		if want(r) {
			return r, nil
		}
		c.opts.Log.Debug("device_response_skipped", "command", command, "response", r.Raw)
	}
}
