package dive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/loon-rocks/dive-go/internal/wire"
	"github.com/loon-rocks/dive-go/token"
)

// Version is the version of this module. The default User-Agent of a
// [Client] is "dive-go/" followed by Version.
var Version = "0.1.0"

// Client calls the HTTP API of a dive serve instance: it lists plans,
// starts runs, reads and cancels them, and follows their entries.
// Create one with [NewClient].
//
// Every method takes the run token that authorizes the request and sends
// it as a Bearer token. Mint tokens with [token.Signer]: a start token for
// [Client.Start], and a read token with the plans:read, runs:read or
// runs:cancel scope for the other methods.
//
// A Client is safe for concurrent use by multiple goroutines.
type Client struct {
	base      string
	http      *http.Client
	userAgent string
}

// ClientOption configures a [Client] in [NewClient].
type ClientOption func(*Client)

// WithHTTPClient sets the HTTP client that sends every request. When it is
// omitted, the Client uses a new [net/http.Client] with no timeout.
//
// A Timeout on c also limits each connection of [Client.Entries], which
// stays open while a run is going; Entries then reconnects.
func WithHTTPClient(c *http.Client) ClientOption {
	return func(client *Client) { client.http = c }
}

// WithUserAgent sets the User-Agent header of every request. When it is
// omitted, the header is "dive-go/" followed by [Version].
func WithUserAgent(ua string) ClientOption {
	return func(client *Client) { client.userAgent = ua }
}

// NewClient returns a Client for the dive serve instance at baseURL, such
// as "http://127.0.0.1:8080". NewClient trims trailing slashes from
// baseURL and appends API paths such as /v1/plans to it. It doesn't
// contact the server.
func NewClient(baseURL string, opts ...ClientOption) *Client {
	c := &Client{
		base:      strings.TrimRight(baseURL, "/"),
		http:      &http.Client{},
		userAgent: "dive-go/" + Version,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Status is the state of a run, as [Run.Status] and [RunView.Status]
// report it. A run starts as [StatusRunning] and ends with one of the
// other statuses.
type Status string

// The statuses of a run.
const (
	// StatusRunning means the run is still going.
	StatusRunning Status = "running"
	// StatusCompleted means the run finished. [RunView.Outcome] says how.
	StatusCompleted Status = "completed"
	// StatusFailed means the run stopped on an error. [RunView.Error] has
	// the message.
	StatusFailed Status = "failed"
	// StatusCancelled means the run stopped after [Client.Cancel].
	StatusCancelled Status = "cancelled"
	// StatusInterrupted means the process running the run stopped or crashed
	// before the run finished.
	StatusInterrupted Status = "interrupted"
)

// Done reports whether s is a final status: [StatusCompleted],
// [StatusFailed], [StatusCancelled] or [StatusInterrupted]. It is false
// for [StatusRunning], the empty string and statuses this package
// doesn't define.
func (s Status) Done() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusInterrupted:
		return true
	}
	return false
}

// Outcome says how a completed run ended. It is empty unless the run's
// status is [StatusCompleted].
type Outcome string

// The outcomes of a completed run.
const (
	// OutcomeOK means the plan finished and produced its result.
	OutcomeOK Outcome = "ok"
	// OutcomeExitGate means an exit gate with status error ended the plan.
	// [RunView.Error] carries the gate's message.
	OutcomeExitGate Outcome = "exit_gate"
)

// Plan describes a plan that dive serve can run, as [Client.Plans] and
// [Client.Plan] return it.
type Plan struct {
	// Identifier names the plan in [Client.Start] and in API paths.
	Identifier string

	// Name is the plan's human-readable name.
	Name string

	// Description says what the plan does.
	Description string

	// InputSchema is the JSON Schema of the plan's input, or nil when the
	// plan doesn't declare one.
	InputSchema json.RawMessage

	// ResultSchema is the JSON Schema of the plan's result, or nil when the
	// plan doesn't declare an output_schema.
	ResultSchema json.RawMessage
}

// Run identifies a run that [Client.Start] started.
type Run struct {
	// ID is the run's id: the jti of the start token.
	ID string

	// SessionID is the id of the session the run belongs to.
	SessionID string

	// Status is the run's status when dive serve accepted it, normally
	// [StatusRunning]. It is empty in [AlreadyStartedError.Run].
	Status Status

	// EntriesURL is the path of the run's entry stream, such as
	// /v1/runs/{id}/entries.
	EntriesURL string
}

// Usage is the model usage of a run, as dive serve records it in the
// run_ended entry.
type Usage struct {
	// Calls is the number of model calls the run made.
	Calls int64 `json:"calls"`

	// InputTokens is the number of input tokens across all model calls.
	InputTokens int64 `json:"input_tokens"`

	// OutputTokens is the number of output tokens across all model calls.
	OutputTokens int64 `json:"output_tokens"`

	// CostUSD is the run's cost in US dollars, or nil when the dive serve
	// configuration doesn't price the models used.
	CostUSD *float64 `json:"cost_usd,omitempty"`

	// ByModel is the usage per model, as raw JSON.
	ByModel json.RawMessage `json:"by_model,omitempty"`

	// ByStep is the usage per plan step, as raw JSON.
	ByStep json.RawMessage `json:"by_step,omitempty"`
}

// RunView is the state of a run, as [Client.Run], [Client.Cancel] and
// [Client.Wait] return it.
type RunView struct {
	// ID is the run's id.
	ID string

	// SessionID is the id of the session the run belongs to.
	SessionID string

	// Plan is the identifier of the plan the run executes.
	Plan string

	// Status is the run's status.
	Status Status

	// Outcome says how a completed run ended. It is empty unless Status is
	// [StatusCompleted].
	Outcome Outcome

	// Result is the plan's output as JSON, or nil when the run has none.
	// Decode it with [Result].
	Result json.RawMessage

	// Error is the error message of a failed run, or the message of the exit
	// gate that ended it. It is empty otherwise.
	Error string

	// FirstSeq is the seq of the run's first entry.
	FirstSeq uint64

	// LastSeq is the seq of the run's last entry, or nil while the run is
	// going.
	LastSeq *uint64

	// CancelRequested reports whether someone asked to cancel the run.
	CancelRequested bool

	// Usage is the run's model usage, or nil when dive serve has none to
	// report, as while the run is going.
	Usage *Usage

	// StartedAt is when the run started.
	StartedAt time.Time

	// EndedAt is when the run ended, or nil while it is going.
	EndedAt *time.Time
}

// Result decodes the result of a run into a T with [encoding/json].
// It returns an error when view is nil, when the run has no result,
// for example because it failed or is still going, and when the result
// doesn't decode into a T.
func Result[T any](view *RunView) (T, error) {
	var out T
	if view == nil {
		return out, errors.New("dive: no run to read a result from")
	}
	if len(view.Result) == 0 || string(view.Result) == "null" {
		return out, fmt.Errorf("dive: run %s has no result (status %s)", view.ID, view.Status)
	}
	if err := json.Unmarshal(view.Result, &out); err != nil {
		return out, fmt.Errorf("dive: decoding the result of run %s: %w", view.ID, err)
	}
	return out, nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, tok token.Token, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("dive: encoding the request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, fmt.Errorf("dive: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+string(tok))
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) call(ctx context.Context, method, path string, tok token.Token, body any, want int, out any) error {
	req, err := c.newRequest(ctx, method, path, tok, body)
	if err != nil {
		return err
	}
	return c.do(req, want, out)
}

func (c *Client) do(req *http.Request, want int, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dive: %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		return readError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("dive: decoding %s %s: %w", req.Method, req.URL.Path, err)
	}
	return nil
}

// Plans lists the plans dive serve can run. tok needs the plans:read
// scope.
func (c *Client) Plans(ctx context.Context, tok token.Token) ([]Plan, error) {
	var list wire.PlanList
	if err := c.call(ctx, http.MethodGet, "/v1/plans", tok, nil, http.StatusOK, &list); err != nil {
		return nil, err
	}
	plans := make([]Plan, len(list.Plans))
	for i, p := range list.Plans {
		plans[i] = planFrom(p)
	}
	return plans, nil
}

// Plan returns the plan with the given identifier. tok needs the
// plans:read scope. An unknown plan is an [*APIError] with
// [CodeNotFound].
func (c *Client) Plan(ctx context.Context, tok token.Token, plan string) (*Plan, error) {
	var body wire.PlanBody
	if err := c.call(ctx, http.MethodGet, "/v1/plans/"+url.PathEscape(plan), tok, nil, http.StatusOK, &body); err != nil {
		return nil, err
	}
	p := planFrom(body)
	return &p, nil
}

func planFrom(p wire.PlanBody) Plan {
	return Plan{Identifier: p.Identifier, Name: p.Name, Description: p.Description, InputSchema: present(p.InputSchema), ResultSchema: present(p.ResultSchema)}
}

func present(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

// StartOption configures a run in [Client.Start].
type StartOption func(*wire.StartBody)

// Title sets the title of the session a run starts. When it is omitted,
// dive serve uses the plan's name.
func Title(title string) StartOption {
	return func(b *wire.StartBody) { b.Title = &title }
}

// Start starts a run of plan and returns as soon as dive serve accepts
// it, without waiting for the run to finish. Follow the run with
// [Client.Entries] or [Client.Wait].
//
// tok is a start token from [token.Signer.Start] for the same plan. Its
// jti becomes the run's id. Start encodes input with [encoding/json];
// a nil input is sent as an empty object. The input must match the
// plan's input schema, or Start returns an [*APIError] with
// [CodeInvalidInput] whose Problems list why.
//
// A start token starts one run. If tok already started a run, Start
// returns an [*AlreadyStartedError] naming that run, so a start whose
// response was lost can be retried with the same token. When dive serve
// is at its run limit, Start returns an [*AtCapacityError]. Other error
// responses are [*APIError] values.
func (c *Client) Start(ctx context.Context, tok token.Token, plan string, input any, opts ...StartOption) (*Run, error) {
	if input == nil {
		input = map[string]any{}
	}
	body := wire.StartBody{Input: input}
	for _, opt := range opts {
		opt(&body)
	}
	var started wire.StartedBody
	if err := c.call(ctx, http.MethodPost, "/v1/plans/"+url.PathEscape(plan)+"/runs", tok, body, http.StatusAccepted, &started); err != nil {
		var api *APIError
		if errors.As(err, &api) && api.Code == CodeIdempotencyViolation && api.RunID != "" {
			return nil, &AlreadyStartedError{
				Run: Run{ID: api.RunID, SessionID: api.SessionID, EntriesURL: "/v1/runs/" + url.PathEscape(api.RunID) + "/entries"},
				Err: api,
			}
		}
		return nil, err
	}
	return &Run{ID: started.RunID, SessionID: started.SessionID, Status: Status(started.Status), EntriesURL: started.EntriesURL}, nil
}

// Run returns the current state of the run with the given id. tok needs
// the runs:read scope. A run that doesn't exist, or belongs to another
// tenant, is an [*APIError] with [CodeNotFound].
func (c *Client) Run(ctx context.Context, tok token.Token, runID string) (*RunView, error) {
	return c.runView(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(runID), tok, http.StatusOK)
}

// Cancel asks dive serve to stop a run after the tool call in progress,
// and returns the run as it stands. tok needs the runs:cancel scope.
//
// Cancel doesn't wait for the run to stop: the returned view usually
// still has [StatusRunning] with CancelRequested set. Call [Client.Wait]
// to see the run end with [StatusCancelled]. Cancelling a finished run
// leaves it as it is.
func (c *Client) Cancel(ctx context.Context, tok token.Token, runID string) (*RunView, error) {
	return c.runView(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(runID)+"/cancel", tok, http.StatusAccepted)
}

func (c *Client) runView(ctx context.Context, method, path string, tok token.Token, want int) (*RunView, error) {
	var body wire.RunBody
	if err := c.call(ctx, method, path, tok, nil, want, &body); err != nil {
		return nil, err
	}
	return viewFrom(body)
}

// Wait blocks until a run ends and returns its final state. tok needs
// the runs:read scope.
//
// If the run has already ended, Wait returns at once. Otherwise it
// follows the run's entries with [Client.Entries], passing opts, until
// the run_ended entry, then reads the run again. Wait returns the first
// error Entries yields, including ctx's error when ctx is done.
func (c *Client) Wait(ctx context.Context, tok token.Token, runID string, opts ...EntriesOption) (*RunView, error) {
	view, err := c.Run(ctx, tok, runID)
	if err != nil {
		return nil, err
	}
	if view.Status.Done() {
		return view, nil
	}
	for _, err := range c.Entries(ctx, tok, runID, opts...) {
		if err != nil {
			return nil, err
		}
	}
	return c.Run(ctx, tok, runID)
}

func viewFrom(b wire.RunBody) (*RunView, error) {
	v := &RunView{
		ID:              b.ID,
		SessionID:       b.SessionID,
		Status:          Status(b.Status),
		Result:          present(b.Result),
		FirstSeq:        b.FirstSeq,
		LastSeq:         b.LastSeq,
		CancelRequested: b.CancelRequested,
	}
	if b.Plan != nil {
		v.Plan = *b.Plan
	}
	if b.Outcome != nil {
		v.Outcome = Outcome(*b.Outcome)
	}
	if b.Error != nil {
		v.Error = *b.Error
	}
	if len(b.Usage) > 0 && string(b.Usage) != "null" {
		var u Usage
		if err := json.Unmarshal(b.Usage, &u); err != nil {
			return nil, fmt.Errorf("dive: decoding the usage of run %s: %w", b.ID, err)
		}
		v.Usage = &u
	}
	if b.StartedAt != "" {
		t, err := time.Parse(time.RFC3339, b.StartedAt)
		if err != nil {
			return nil, fmt.Errorf("dive: run %s started_at: %w", b.ID, err)
		}
		v.StartedAt = t
	}
	if b.EndedAt != nil && *b.EndedAt != "" {
		t, err := time.Parse(time.RFC3339, *b.EndedAt)
		if err != nil {
			return nil, fmt.Errorf("dive: run %s ended_at: %w", b.ID, err)
		}
		v.EndedAt = &t
	}
	return v, nil
}
