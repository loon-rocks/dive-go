package wire

import "encoding/json"

// EntryDoc is the part of a run entry common to every kind.
type EntryDoc struct {
	Seq    uint64  `json:"seq"`
	RunID  string  `json:"run_id"`
	At     int64   `json:"at"`
	Author string  `json:"author"`
	Kind   string  `json:"kind"`
	Parent *uint64 `json:"parent,omitempty"`
	Path   *string `json:"path,omitempty"`
}

// ErrorBody is the body of an error response.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the error object inside an [ErrorBody].
type ErrorDetail struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Problems  []string `json:"problems,omitempty"`
	RunID     *string  `json:"run_id,omitempty"`
	SessionID *string  `json:"session_id,omitempty"`
}

// PlanBody describes one plan.
type PlanBody struct {
	Identifier   string          `json:"identifier"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	ResultSchema json.RawMessage `json:"result_schema,omitempty"`
}

// PlanList is the body of GET /v1/plans.
type PlanList struct {
	Plans []PlanBody `json:"plans"`
}

// RunBody is the state of a run, from GET /v1/runs/{run} and
// POST /v1/runs/{run}/cancel.
type RunBody struct {
	ID              string          `json:"id"`
	SessionID       string          `json:"session_id"`
	Plan            *string         `json:"plan,omitempty"`
	Status          string          `json:"status"`
	Outcome         *string         `json:"outcome,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *string         `json:"error,omitempty"`
	FirstSeq        uint64          `json:"first_seq"`
	LastSeq         *uint64         `json:"last_seq,omitempty"`
	CancelRequested bool            `json:"cancel_requested"`
	Usage           json.RawMessage `json:"usage,omitempty"`
	StartedAt       string          `json:"started_at"`
	EndedAt         *string         `json:"ended_at,omitempty"`
}

// StartBody is the request body of POST /v1/plans/{plan}/runs.
type StartBody struct {
	Input any     `json:"input"`
	Title *string `json:"title,omitempty"`
}

// StartedBody is the response body of POST /v1/plans/{plan}/runs.
type StartedBody struct {
	RunID      string `json:"run_id"`
	SessionID  string `json:"session_id"`
	Status     string `json:"status"`
	EntriesURL string `json:"entries_url"`
}

// Delta is the data of a delta event in an entry stream.
type Delta struct {
	Text string `json:"text"`
}
