// Package capture records raw protocol exchanges with every device in a design, so
// the behaviour of real hardware can be checked against the documented protocol and
// turned into test fixtures.
package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/csp"
	"github.com/jacobgad/bose-control-space-controller/internal/design"
)

// Options configure a capture run.
type Options struct {
	Design  design.Design
	Dir     string
	Dial    csp.Dialer
	Timeout time.Duration
	Log     *slog.Logger
	Now     func() time.Time
}

// Result summarises one device's capture.
type Result struct {
	Device        string `json:"device"`
	NodeID        string `json:"node_id"`
	Address       string `json:"address"`
	Type          string `json:"type"`
	Reachable     bool   `json:"reachable"`
	Subscriptions string `json:"subscription_probe"`
	Commands      int    `json:"commands"`
	Responses     int    `json:"responses"`
	NAKs          int    `json:"naks"`
	Timeouts      int    `json:"timeouts"`
	Error         string `json:"error,omitempty"`
	Transcript    string `json:"transcript"`
}

// Run captures every device sequentially and writes transcripts plus a summary.
func Run(ctx context.Context, opts Options) ([]Result, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Timeout == 0 {
		opts.Timeout = 3 * time.Second
	}
	dir := filepath.Join(opts.Dir, opts.Now().UTC().Format("2006-01-02T15-04-05Z"))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("capture: create %s: %w", dir, err)
	}
	results := make([]Result, 0, len(opts.Design.Devices))
	for _, dev := range opts.Design.Devices {
		if ctx.Err() != nil {
			break
		}
		results = append(results, captureDevice(ctx, opts, dir, dev))
	}
	summary, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), summary, 0o600); err != nil {
		return results, err
	}
	opts.Log.Info("capture_complete", "dir", dir, "devices", len(results))
	return results, nil
}

type recorder struct {
	mu        sync.Mutex
	w         io.Writer
	now       func() time.Time
	sent      int
	received  int
	naks      int
	lastRecvd time.Time
}

func (r *recorder) Sent(_ string, command string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent++
	_, _ = fmt.Fprintf(r.w, "%s > %s\n", r.now().UTC().Format(time.RFC3339Nano), command)
}

func (r *recorder) Received(_ string, raw []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.received++
	r.lastRecvd = r.now()
	if len(raw) > 0 && raw[0] == csp.NAK {
		r.naks++
	}
	_, _ = fmt.Fprintf(r.w, "%s < %s  |%s|\n", r.now().UTC().Format(time.RFC3339Nano), hexDump(raw), printable(raw))
}

func (r *recorder) note(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = fmt.Fprintf(r.w, "# "+format+"\n", args...)
}

func hexDump(b []byte) string {
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = fmt.Sprintf("%02x", c)
	}
	return strings.Join(parts, " ")
}

func printable(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == '\r':
			sb.WriteString(`\r`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == csp.ACK:
			sb.WriteString("<ACK>")
		case c == csp.NAK:
			sb.WriteString("<NAK>")
		case c < 0x20 || c > 0x7e:
			fmt.Fprintf(&sb, `\x%02x`, c)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
}

func captureDevice(ctx context.Context, opts Options, dir string, dev design.Device) Result {
	res := Result{Device: dev.Label, NodeID: dev.NodeID, Address: dev.Address(), Type: string(dev.Type)}
	path := filepath.Join(dir, safeName(dev.Label)+"-"+dev.NodeID+".log")
	res.Transcript = path
	f, err := os.Create(path) //nolint:gosec // path is derived from the operator's capture_dir
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer func() {
		if err := f.Close(); err != nil {
			opts.Log.Warn("capture_transcript_close_failed", "path", path, "error", err.Error())
		}
	}()
	rec := &recorder{w: f, now: opts.Now}
	rec.note("device %s (%s) nodeID=%s type=%s model=%s firmware=%s", dev.Label, dev.Address(), dev.NodeID, dev.Type, dev.Model, dev.Firmware)
	log := opts.Log.With("device", dev.Label, "address", dev.Address())

	client := csp.NewClient(csp.ClientOptions{Address: dev.Address(), Dial: opts.Dial, Timeout: opts.Timeout, Log: log, Transcript: rec})
	defer client.Close()
	if err := client.Connect(ctx); err != nil {
		res.Error = err.Error()
		rec.note("connect failed: %v", err)
		log.Warn("capture_unreachable", "error", err.Error())
		return res
	}
	res.Reachable = true

	probe := func(command string) csp.Response {
		r, err := client.Raw(ctx, command)
		switch {
		case err != nil:
			rec.note("%s: error %v", command, err)
			_ = client.Connect(ctx)
		case r.Kind == csp.KindNAK:
			res.NAKs++
		case r.Raw == "" && r.Kind == csp.KindLine:
			res.Timeouts++
			rec.note("%s: no response within %s", command, opts.Timeout)
		}
		return r
	}

	sub := probe(csp.ProbeSubscription)
	res.Subscriptions = strings.TrimSpace(sub.Raw)
	if res.Subscriptions == "" {
		res.Subscriptions = "no response"
	}
	if dev.IsMain {
		probe(csp.GetParameterSet)
	}
	if dev.Type == design.DevicePowerMatch {
		probe(csp.GetAmpConfiguration)
		probe(csp.GetAmpStandby)
		probe(csp.GetAmpFaultStatus)
	}

	blocks := opts.Design.BlocksOn(dev.NodeID)
	for _, blk := range blocks {
		for _, idx := range indices(blk.Kind) {
			probe(csp.GetModule(blk.Label, idx))
		}
	}
	writeBack(ctx, client, rec, blocks)

	rec.mu.Lock()
	res.Commands, res.Responses = rec.sent, rec.received
	rec.mu.Unlock()
	log.Info("capture_device_done", "commands", res.Commands, "responses", res.Responses, "naks", res.NAKs, "timeouts", res.Timeouts, "subscriptions", res.Subscriptions)
	return res
}

// writeBack re-sends the first block's current level and mute unchanged, which is
// harmless but shows exactly how the device acknowledges SA.
func writeBack(ctx context.Context, client *csp.Client, rec *recorder, blocks []design.Block) {
	if len(blocks) == 0 {
		return
	}
	blk := blocks[0]
	levelIdx, muteIdx := indices(blk.Kind)[0], indices(blk.Kind)[1]
	level, err := client.Get(ctx, blk.Label, levelIdx)
	if err != nil {
		rec.note("write-back skipped: %v", err)
		return
	}
	if _, err := csp.ParseLevel(level); err != nil {
		rec.note("write-back skipped: level %q unparseable", level)
		return
	}
	if err := client.Set(ctx, blk.Label, level, levelIdx); err != nil {
		rec.note("write-back SA level: %v", err)
		var nak *csp.NAKError
		if !errors.As(err, &nak) {
			return
		}
	}
	mute, err := client.Get(ctx, blk.Label, muteIdx)
	if err != nil {
		rec.note("write-back mute read: %v", err)
		return
	}
	if err := client.Set(ctx, blk.Label, strings.TrimSpace(mute), muteIdx); err != nil {
		rec.note("write-back SA mute: %v", err)
	}
}

func indices(kind design.BlockKind) []int {
	switch kind {
	case design.KindGain:
		return []int{csp.GainLevel, csp.GainMute}
	case design.KindInput:
		return []int{csp.InputLevel, csp.InputMute, csp.InputGain, csp.InputPhantom, 1}
	case design.KindAmpOutput:
		return []int{csp.AmpOutputLevel, csp.AmpOutputMute, 3}
	}
	return nil
}

// Summarize renders the results as a log-friendly table.
func Summarize(results []Result) string {
	var sb strings.Builder
	for _, r := range results {
		status := "unreachable"
		if r.Reachable {
			status = "ok cmds=" + strconv.Itoa(r.Commands) + " resp=" + strconv.Itoa(r.Responses) + " naks=" + strconv.Itoa(r.NAKs) + " timeouts=" + strconv.Itoa(r.Timeouts) + " SUB=" + strconv.Quote(r.Subscriptions)
		}
		fmt.Fprintf(&sb, "%-14s %-22s %s\n", r.Device, r.Address, status)
	}
	return sb.String()
}
