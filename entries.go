package dive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/loon-rocks/dive-go/internal/wire"
	"github.com/loon-rocks/dive-go/token"
)

// These are the defaults [Client.Entries] uses when [MaxReconnects] or
// [ReconnectDelay] is omitted.
const (
	DefaultMaxReconnects  = 5
	DefaultReconnectDelay = 500 * time.Millisecond
	maxReconnectDelay     = 10 * time.Second
)

// KindRunEnded is the [Entry.Kind] of the last entry of every run.
// [Client.Entries] stops after yielding it.
const KindRunEnded = "run_ended"

// Entry is one entry of a run's log, as [Client.Entries] yields it.
// The fields common to every entry are decoded; the rest depend on Kind
// and are in Raw.
type Entry struct {
	// Seq is the entry's position in the session's log, counting from 1.
	// A run's entries have consecutive seqs.
	Seq uint64

	// RunID is the id of the run the entry belongs to.
	RunID string

	// Path is where in the run the entry was written, such as "step:E3".
	// It is empty for entries written at the top level.
	Path string

	// At is when the entry was written, in milliseconds since the Unix epoch.
	// [Entry.Time] returns it as a [time.Time].
	At int64

	// Author is "user", "run", or the name of the agent that wrote the entry.
	Author string

	// Kind is what the entry is, such as "run_started", "step_finished" or
	// [KindRunEnded].
	Kind string

	// Parent is the seq of the entry this one belongs under, or nil.
	Parent *uint64

	// Raw is the whole entry as JSON, including the fields specific to Kind.
	Raw json.RawMessage
}

// Time returns [Entry.At] as a [time.Time] in the local time zone.
func (e Entry) Time() time.Time { return time.UnixMilli(e.At) }

// Decode unmarshals the whole entry, [Entry.Raw], into v with
// [encoding/json]. Use it to read the fields specific to an entry's Kind.
func (e Entry) Decode(v any) error { return json.Unmarshal(e.Raw, v) }

// Delta is a piece of model text streamed while a run is going.
// [WithDeltas] receives them. Deltas aren't entries: they have no seq,
// dive serve doesn't store them, and a reconnect doesn't replay them.
type Delta struct {
	// Text is the streamed text.
	Text string
}

type entriesConfig struct {
	after         uint64
	hasAfter      bool
	deltas        func(Delta)
	maxReconnects int
	delay         time.Duration
}

// EntriesOption configures [Client.Entries] and [Client.Wait].
type EntriesOption func(*entriesConfig)

// After makes [Client.Entries] start after the entry with the given seq,
// so the first entry it yields has a higher seq. When it is omitted, the
// stream starts at the run's first entry.
func After(seq uint64) EntriesOption {
	return func(c *entriesConfig) { c.after, c.hasAfter = seq, true }
}

// WithDeltas calls fn with each [Delta] the stream carries. fn runs on
// the goroutine that ranges over the iterator, between entries, so it
// should return quickly. When it is omitted, deltas are dropped.
func WithDeltas(fn func(Delta)) EntriesOption {
	return func(c *entriesConfig) { c.deltas = fn }
}

// MaxReconnects sets how many times in a row [Client.Entries] reconnects
// after the stream fails. The count starts again whenever a connection
// delivers a new entry. Zero means Entries doesn't reconnect. When it is
// omitted, the limit is [DefaultMaxReconnects].
func MaxReconnects(n int) EntriesOption {
	return func(c *entriesConfig) { c.maxReconnects = n }
}

// ReconnectDelay sets how long [Client.Entries] waits before its first
// reconnect. The wait doubles with each further failure in a row, up to
// 10 seconds. A retry field in the event stream replaces d for the rest
// of that iteration only. When it is omitted, the delay is
// [DefaultReconnectDelay].
func ReconnectDelay(d time.Duration) EntriesOption {
	return func(c *entriesConfig) { c.delay = d }
}

var errStreamClosed = errors.New("the entry stream closed before run_ended")

// Entries returns an iterator over the entries of a run, in seq order.
// It follows the run's server-sent event stream until the run_ended
// entry, which it yields last, so a running run is followed live and a
// finished run is replayed. tok needs the runs:read scope. Entries
// connects when iteration starts, not when it is called, and breaking
// out of the loop closes the connection.
//
// When the connection drops, or dive serve answers with a 5xx status,
// Entries reconnects with a Last-Event-ID header set to the last seq it
// yielded, so no entry is missed or yielded twice. See [MaxReconnects]
// and [ReconnectDelay].
//
// An error ends the iteration: Entries yields a zero Entry with the error
// and stops. The error is ctx's error when ctx is done, an [*APIError]
// when dive serve answers with a 4xx status, and otherwise the last
// connection error once the reconnects run out.
func (c *Client) Entries(ctx context.Context, tok token.Token, runID string, opts ...EntriesOption) iter.Seq2[Entry, error] {
	cfg := entriesConfig{maxReconnects: DefaultMaxReconnects, delay: DefaultReconnectDelay}
	for _, opt := range opts {
		opt(&cfg)
	}
	return func(yield func(Entry, error) bool) {
		run := cfg
		s := &stream{client: c, tok: tok, runID: runID, cfg: &run, last: run.after, hasLast: run.hasAfter}
		failures := 0
		for {
			progressed, stop, err := s.follow(ctx, yield)
			if stop {
				return
			}
			if ctx.Err() != nil {
				yield(Entry{}, ctx.Err())
				return
			}
			if !retryable(err) {
				yield(Entry{}, err)
				return
			}
			if progressed {
				failures = 0
			}
			failures++
			if failures > run.maxReconnects {
				yield(Entry{}, fmt.Errorf("dive: gave up on the entries of run %s after %d reconnects: %w", runID, run.maxReconnects, err))
				return
			}
			delay := min(run.delay*time.Duration(1<<(failures-1)), maxReconnectDelay)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				yield(Entry{}, ctx.Err())
				return
			case <-timer.C:
			}
		}
	}
}

func retryable(err error) bool {
	var api *APIError
	if errors.As(err, &api) {
		return api.Status >= 500
	}
	return true
}

type stream struct {
	client  *Client
	tok     token.Token
	runID   string
	cfg     *entriesConfig
	last    uint64
	hasLast bool
}

func (s *stream) follow(ctx context.Context, yield func(Entry, error) bool) (progressed, stop bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.client.base+"/v1/runs/"+url.PathEscape(s.runID)+"/entries", nil)
	if err != nil {
		return false, false, fmt.Errorf("dive: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+string(s.tok))
	req.Header.Set("User-Agent", s.client.userAgent)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if s.hasLast {
		req.Header.Set("Last-Event-ID", strconv.FormatUint(s.last, 10))
	}
	resp, err := s.client.http.Do(req)
	if err != nil {
		return false, false, fmt.Errorf("dive: watching run %s: %w", s.runID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false, readError(resp)
	}
	if media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); media != "text/event-stream" {
		return false, false, &APIError{Status: resp.StatusCode, Code: CodeInternal, Message: "the entries response isn't an event stream"}
	}
	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	var event string
	var data strings.Builder
	hasData := false
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !(errors.Is(readErr, io.EOF) && line != "") {
			if errors.Is(readErr, io.EOF) {
				return progressed, false, fmt.Errorf("dive: run %s: %w", s.runID, errStreamClosed)
			}
			return progressed, false, fmt.Errorf("dive: reading the entries of run %s: %w", s.runID, readErr)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if hasData || event != "" {
				done, more := s.dispatch(event, data.String(), yield)
				if done || !more {
					return progressed, true, nil
				}
				if event == "entry" || event == "" {
					progressed = true
				}
			}
			event = ""
			data.Reset()
			hasData = false
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			if hasData {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasData = true
		case "retry":
			if ms, err := strconv.Atoi(value); err == nil && ms >= 0 {
				s.cfg.delay = time.Duration(ms) * time.Millisecond
			}
		}
	}
}

func (s *stream) dispatch(event, data string, yield func(Entry, error) bool) (done, more bool) {
	switch event {
	case "delta":
		if s.cfg.deltas != nil {
			var d wire.Delta
			if json.Unmarshal([]byte(data), &d) == nil {
				s.cfg.deltas(Delta{Text: d.Text})
			}
		}
		return false, true
	case "entry", "":
		var doc wire.EntryDoc
		if err := json.Unmarshal([]byte(data), &doc); err != nil {
			return true, yield(Entry{}, fmt.Errorf("dive: decoding an entry of run %s: %w", s.runID, err))
		}
		if s.hasLast && doc.Seq <= s.last {
			return false, true
		}
		s.last, s.hasLast = doc.Seq, true
		entry := Entry{Seq: doc.Seq, RunID: doc.RunID, At: doc.At, Author: doc.Author, Kind: doc.Kind, Parent: doc.Parent, Raw: json.RawMessage(data)}
		if doc.Path != nil {
			entry.Path = *doc.Path
		}
		if !yield(entry, nil) {
			return true, false
		}
		return entry.Kind == KindRunEnded, true
	}
	return false, true
}
