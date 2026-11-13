package dive_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	dive "github.com/loon-rocks/dive-go"
	"github.com/loon-rocks/dive-go/token"
)

const recordedRun = "28e67bc9-d9e7-4995-ab9e-fbb663d6592b"

const fakeToken = token.Token("header.payload.signature")

type recorded struct {
	seq   uint64
	block string
}

func loadRecording(t *testing.T) []recorded {
	t.Helper()
	data, err := os.ReadFile("testdata/greet.sse")
	if err != nil {
		t.Fatal(err)
	}
	var out []recorded
	for _, block := range strings.Split(strings.TrimSpace(string(data)), "\n\n") {
		var seq uint64
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "id: "); ok {
				seq, _ = strconv.ParseUint(v, 10, 64)
			}
		}
		out = append(out, recorded{seq: seq, block: block + "\n\n"})
	}
	return out
}

type fakeServe struct {
	t          *testing.T
	recording  []recorded
	mu         sync.Mutex
	conns      int
	lastIDs    []string
	dropAfter  []int
	alwaysDrop bool
	status     string
	auth       []string
	starts     []map[string]any
}

func (f *fakeServe) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/runs/{run}/entries", f.entries)
	mux.HandleFunc("GET /v1/runs/{run}", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if r.PathValue("run") != recordedRun {
			writeAPIError(w, 404, "not_found", "no run "+r.PathValue("run"), nil)
			return
		}
		f.mu.Lock()
		status := f.status
		f.status = "completed"
		f.mu.Unlock()
		if status == "running" {
			fmt.Fprintf(w, `{"id":%q,"session_id":"349d2ec8f590","plan":"greet","status":"running","outcome":null,"result":null,"error":null,"first_seq":1,"last_seq":null,"cancel_requested":false,"usage":null,"started_at":"2026-11-13T04:21:25Z","ended_at":null}`, recordedRun)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"session_id":"349d2ec8f590","plan":"greet","status":"completed","outcome":"ok","result":{"greeting":"hello ada"},"error":null,"first_seq":1,"last_seq":4,"cancel_requested":false,"usage":{"by_model":[],"by_step":[],"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"calls":2,"input_tokens":120,"output_image_tokens":0,"output_tokens":30,"cost_usd":0.01},"started_at":"2026-11-13T04:21:25Z","ended_at":"2026-11-13T04:21:26Z"}`, recordedRun)
	})
	mux.HandleFunc("POST /v1/runs/{run}/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"id":%q,"session_id":"349d2ec8f590","plan":"greet","status":"running","outcome":null,"result":null,"error":null,"first_seq":1,"last_seq":2,"cancel_requested":true,"usage":null,"started_at":"2026-11-13T04:21:25Z","ended_at":null}`, recordedRun)
	})
	mux.HandleFunc("GET /v1/plans", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		io.WriteString(w, `{"plans":[{"identifier":"greet","name":"Greet","description":"Greets someone.","input_schema":{"type":"object"},"result_schema":null}]}`)
	})
	mux.HandleFunc("POST /v1/plans/{plan}/runs", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.starts = append(f.starts, body)
		f.mu.Unlock()
		input, _ := body["input"].(map[string]any)
		switch input["case"] {
		case "again":
			writeAPIError(w, 409, "idempotency_violation", "run "+recordedRun+" already started", map[string]any{"run_id": recordedRun, "session_id": "349d2ec8f590"})
		case "other-tenant":
			writeAPIError(w, 409, "idempotency_violation", "the jti already started a run", nil)
		case "busy":
			w.Header().Set("Retry-After", "7")
			writeAPIError(w, 429, "at_capacity", "the tenant is at its run limit", nil)
		case "bad":
			writeAPIError(w, 422, "invalid_input", "the input doesn't match", map[string]any{"problems": []string{"/name: required"}})
		case "plain":
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "upstream down")
		default:
			w.Header().Set("Location", "/v1/runs/"+recordedRun)
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"run_id":%q,"session_id":"349d2ec8f590","status":"running","entries_url":"/v1/runs/%s/entries"}`, recordedRun, recordedRun)
		}
	})
	return mux
}

func (f *fakeServe) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
}

func writeAPIError(w http.ResponseWriter, status int, code, message string, extra map[string]any) {
	detail := map[string]any{"code": code, "message": message}
	for k, v := range extra {
		detail[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": detail})
}

func (f *fakeServe) entries(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	if r.PathValue("run") != recordedRun {
		writeAPIError(w, 404, "not_found", "no run", nil)
		return
	}
	f.mu.Lock()
	conn := f.conns
	f.conns++
	f.lastIDs = append(f.lastIDs, r.Header.Get("Last-Event-ID"))
	limit := -1
	if conn < len(f.dropAfter) {
		limit = f.dropAfter[conn]
	}
	if f.alwaysDrop {
		limit = 0
	}
	f.mu.Unlock()
	var after uint64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		after, _ = strconv.ParseUint(v, 10, 64)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	io.WriteString(w, ": heartbeat\n\n")
	sent := 0
	for _, e := range f.recording {
		if e.seq <= after {
			continue
		}
		if limit >= 0 && sent == limit {
			flusher.Flush()
			panic(http.ErrAbortHandler)
		}
		io.WriteString(w, e.block)
		sent++
		if e.seq == 2 {
			io.WriteString(w, "event: delta\ndata: {\"text\":\"hello \"}\n\n")
			io.WriteString(w, "event: delta\ndata: {\"text\":\"ada\"}\n\n")
			io.WriteString(w, ": heartbeat\n\n")
		}
		flusher.Flush()
	}
}

func newFake(t *testing.T) (*fakeServe, *dive.Client) {
	t.Helper()
	f := &fakeServe{t: t, recording: loadRecording(t), status: "completed"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, dive.NewClient(srv.URL + "/")
}

func collect(t *testing.T, c *dive.Client, opts ...dive.EntriesOption) ([]dive.Entry, error) {
	t.Helper()
	var got []dive.Entry
	for entry, err := range c.Entries(context.Background(), fakeToken, recordedRun, opts...) {
		if err != nil {
			return got, err
		}
		got = append(got, entry)
	}
	return got, nil
}

func TestEntriesReadsTheRecordedStream(t *testing.T) {
	f, c := newFake(t)
	var deltas []string
	got, err := collect(t, c, dive.WithDeltas(func(d dive.Delta) { deltas = append(deltas, d.Text) }))
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, e := range got {
		kinds = append(kinds, fmt.Sprintf("%d:%s", e.Seq, e.Kind))
	}
	if strings.Join(kinds, " ") != "1:run_started 2:step_started 3:step_finished 4:run_ended" {
		t.Fatalf("entries %v", kinds)
	}
	if got[1].Path != "step:E0" || got[0].Path != "" || got[1].RunID != recordedRun || got[1].Author != "run" {
		t.Fatalf("entry %+v", got[1])
	}
	if got[0].Time().UnixMilli() != 1794543685254 {
		t.Fatalf("time %v", got[0].Time())
	}
	var finished struct {
		Output map[string]string `json:"output"`
	}
	if err := got[2].Decode(&finished); err != nil || finished.Output["greeting"] != "hello ada" {
		t.Fatalf("decode %v %v", finished, err)
	}
	if strings.Join(deltas, "") != "hello ada" {
		t.Fatalf("deltas %q", deltas)
	}
	if f.lastIDs[0] != "" || f.auth[0] != "Bearer header.payload.signature" {
		t.Fatalf("first request: Last-Event-ID %q auth %q", f.lastIDs[0], f.auth[0])
	}
}

func TestEntriesReconnectsWithLastEventIDWithoutMissingASeq(t *testing.T) {
	f, c := newFake(t)
	f.dropAfter = []int{2, 0, 1}
	got, err := collect(t, c, dive.ReconnectDelay(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range got {
		if e.Seq != uint64(i+1) {
			t.Fatalf("entry %d has seq %d", i, e.Seq)
		}
	}
	if len(got) != 4 {
		t.Fatalf("got %d entries", len(got))
	}
	if strings.Join(f.lastIDs, ",") != ",2,2,3" {
		t.Fatalf("Last-Event-ID per connection %q", f.lastIDs)
	}
}

func TestEntriesAfterStartsFromASeq(t *testing.T) {
	f, c := newFake(t)
	got, err := collect(t, c, dive.After(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != 3 || f.lastIDs[0] != "2" {
		t.Fatalf("got %d entries, Last-Event-ID %q", len(got), f.lastIDs[0])
	}
}

func TestEntriesGivesUpAfterMaxReconnects(t *testing.T) {
	f, c := newFake(t)
	f.alwaysDrop = true
	_, err := collect(t, c, dive.ReconnectDelay(time.Millisecond), dive.MaxReconnects(3))
	if err == nil || !strings.Contains(err.Error(), "after 3 reconnects") {
		t.Fatalf("got %v", err)
	}
	if f.conns != 4 {
		t.Fatalf("connections %d, want 4", f.conns)
	}
}

func TestEntriesStopsOnAnAPIError(t *testing.T) {
	f, c := newFake(t)
	var got error
	for _, err := range c.Entries(context.Background(), fakeToken, "missing") {
		got = err
	}
	var api *dive.APIError
	if !errors.As(got, &api) || api.Code != dive.CodeNotFound || f.conns != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestEntriesStopsWhenTheLoopBreaks(t *testing.T) {
	_, c := newFake(t)
	n := 0
	for _, err := range c.Entries(context.Background(), fakeToken, recordedRun) {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if n == 2 {
			break
		}
	}
	if n != 2 {
		t.Fatal(n)
	}
}

func TestStartSendsTheInputAndReturnsTheRun(t *testing.T) {
	f, c := newFake(t)
	run, err := c.Start(context.Background(), fakeToken, "greet", map[string]string{"name": "ada"}, dive.Title("Greet Ada"))
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != recordedRun || run.SessionID != "349d2ec8f590" || run.Status != dive.StatusRunning {
		t.Fatalf("run %+v", run)
	}
	body, _ := json.Marshal(f.starts[0])
	if string(body) != `{"input":{"name":"ada"},"title":"Greet Ada"}` {
		t.Fatalf("body %s", body)
	}
}

func TestStartErrors(t *testing.T) {
	_, c := newFake(t)
	start := func(kind string) error {
		_, err := c.Start(context.Background(), fakeToken, "greet", map[string]string{"case": kind})
		return err
	}
	var already *dive.AlreadyStartedError
	err := start("again")
	if !errors.As(err, &already) || already.Run.ID != recordedRun || already.Run.SessionID != "349d2ec8f590" {
		t.Fatalf("again: %v", err)
	}
	if errors.Is(err, dive.ErrIdempotencyViolation) {
		t.Fatal("a repeated start in the same tenant isn't ErrIdempotencyViolation")
	}
	err = start("other-tenant")
	if !errors.Is(err, dive.ErrIdempotencyViolation) || errors.As(err, &already) {
		t.Fatalf("other tenant: %v", err)
	}
	var busy *dive.AtCapacityError
	err = start("busy")
	if !errors.As(err, &busy) || busy.RetryAfter != 7*time.Second {
		t.Fatalf("busy: %v", err)
	}
	var api *dive.APIError
	if !errors.As(err, &api) || api.Code != dive.CodeAtCapacity || api.Status != 429 {
		t.Fatalf("busy as APIError: %v", err)
	}
	err = start("bad")
	if !errors.As(err, &api) || api.Code != dive.CodeInvalidInput || len(api.Problems) != 1 || !strings.Contains(err.Error(), "/name: required") {
		t.Fatalf("bad: %v", err)
	}
	err = start("plain")
	if !errors.As(err, &api) || api.Status != 502 || api.Code != dive.CodeInternal || api.Message != "upstream down" {
		t.Fatalf("plain: %v", err)
	}
}

func TestWaitFollowsTheStreamAndDecodesTheResult(t *testing.T) {
	f, c := newFake(t)
	f.status = "running"
	view, err := c.Wait(context.Background(), fakeToken, recordedRun)
	if err != nil {
		t.Fatal(err)
	}
	if f.conns != 1 {
		t.Fatalf("Wait should follow the stream once, got %d", f.conns)
	}
	if view.Status != dive.StatusCompleted || view.Outcome != dive.OutcomeOK || view.Plan != "greet" || view.LastSeq == nil || *view.LastSeq != 4 {
		t.Fatalf("view %+v", view)
	}
	if view.Usage == nil || view.Usage.Calls != 2 || view.Usage.InputTokens != 120 || view.Usage.CostUSD == nil {
		t.Fatalf("usage %+v", view.Usage)
	}
	if view.EndedAt == nil || view.EndedAt.Sub(view.StartedAt) != time.Second {
		t.Fatalf("times %v %v", view.StartedAt, view.EndedAt)
	}
	type greeting struct {
		Greeting string `json:"greeting"`
	}
	result, err := dive.Result[greeting](view)
	if err != nil || result.Greeting != "hello ada" {
		t.Fatalf("result %+v %v", result, err)
	}
	done, err := c.Wait(context.Background(), fakeToken, recordedRun)
	if err != nil || done.Status != dive.StatusCompleted || f.conns != 1 {
		t.Fatalf("a finished run needs no stream: %v %d", err, f.conns)
	}
}

func TestCancelAndPlans(t *testing.T) {
	_, c := newFake(t)
	view, err := c.Cancel(context.Background(), fakeToken, recordedRun)
	if err != nil || !view.CancelRequested || view.Status != dive.StatusRunning {
		t.Fatalf("cancel %+v %v", view, err)
	}
	if _, err := dive.Result[map[string]any](view); err == nil {
		t.Fatal("a running run has no result")
	}
	plans, err := c.Plans(context.Background(), fakeToken)
	if err != nil || len(plans) != 1 || plans[0].Identifier != "greet" || string(plans[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("plans %+v %v", plans, err)
	}
	_, err = c.Run(context.Background(), fakeToken, "missing")
	var api *dive.APIError
	if !errors.As(err, &api) || api.Code != dive.CodeNotFound || api.Message != "no run missing" {
		t.Fatalf("run: %v", err)
	}
}

func TestEntriesForgetsTheServersRetryDelayBetweenIterations(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		n := conns
		mu.Unlock()
		if n == 2 {
			http.Error(w, "busy", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "retry: 5000\n\nevent: entry\nid: 1\ndata: {\"seq\":1,\"run_id\":%q,\"at\":1,\"author\":\"run\",\"kind\":\"run_ended\",\"status\":\"completed\"}\n\n", recordedRun)
	}))
	t.Cleanup(srv.Close)
	seq := dive.NewClient(srv.URL+"/").Entries(context.Background(), fakeToken, recordedRun, dive.ReconnectDelay(time.Millisecond))
	for range 2 {
		started := time.Now()
		for _, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
		}
		if waited := time.Since(started); waited > 2*time.Second {
			t.Fatalf("an iteration waited %s", waited)
		}
	}
}

func TestStatusDoneOnlyForFinalStatuses(t *testing.T) {
	for status, want := range map[dive.Status]bool{
		dive.StatusCompleted: true, dive.StatusFailed: true, dive.StatusCancelled: true, dive.StatusInterrupted: true,
		dive.StatusRunning: false, "": false, "paused": false,
	} {
		if status.Done() != want {
			t.Errorf("%q.Done() = %v", status, !want)
		}
	}
}
