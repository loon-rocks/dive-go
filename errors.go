package dive

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/loon-rocks/dive-go/internal/wire"
)

// Code is the machine-readable error code of an [APIError].
type Code string

// The error codes dive serve returns.
const (
	// CodeBadRequest (400) means the request body isn't valid, or a
	// Last-Event-ID isn't a seq.
	CodeBadRequest Code = "bad_request"
	// CodeInvalidToken (401) means the run token is missing, malformed,
	// expired, signed by an untrusted key, or has the wrong issuer or
	// audience.
	CodeInvalidToken Code = "invalid_token"
	// CodeForbidden (403) means the token lacks a scope the request needs,
	// or its plan claim differs from the plan being started.
	CodeForbidden Code = "forbidden"
	// CodeNotFound (404) means the plan is unknown, or the run doesn't exist
	// or belongs to another tenant.
	CodeNotFound Code = "not_found"
	// CodeIdempotencyViolation (409) means the start token's jti already
	// started a run. See [AlreadyStartedError] and [ErrIdempotencyViolation].
	CodeIdempotencyViolation Code = "idempotency_violation"
	// CodeInvalidInput (422) means the run input doesn't match the plan's
	// input schema. [APIError.Problems] lists why.
	CodeInvalidInput Code = "invalid_input"
	// CodeAtCapacity (429) means the process or the tenant is at its run
	// limit. See [AtCapacityError].
	CodeAtCapacity Code = "at_capacity"
	// CodeShuttingDown (503) means the dive serve process is stopping.
	CodeShuttingDown Code = "shutting_down"
	// CodePlanUnavailable (503) means the plan needs an MCP server or tool
	// that the deployment doesn't have.
	CodePlanUnavailable Code = "plan_unavailable"
	// CodeInternal means any other failure, including a response whose
	// status this package doesn't map to a code.
	CodeInternal Code = "internal"
)

// ErrIdempotencyViolation matches, with [errors.Is], the error
// [Client.Start] returns when the start token's jti already started a
// run that dive serve doesn't name, as when the run belongs to another
// tenant. When dive serve names the run, Start returns an
// [*AlreadyStartedError] instead.
var ErrIdempotencyViolation = errors.New("dive: the start token's jti already started a run, and dive serve didn't name it")

// APIError is an error response from dive serve. [Client] methods return
// one when dive serve answers with an unexpected status; use [errors.As]
// to read it, including from an [*AlreadyStartedError] or
// [*AtCapacityError], which wrap one.
type APIError struct {
	// Status is the HTTP status code.
	Status int

	// Code is the error code from the response body. When the body isn't a
	// dive serve error, Code is derived from Status.
	Code Code

	// Message is the human-readable message from the response body, or the
	// body itself when it isn't a dive serve error.
	Message string

	// RunID names the run a start token already started, on a
	// [CodeIdempotencyViolation] error. It is empty otherwise.
	RunID string

	// SessionID names the session of the run in RunID.
	SessionID string

	// Problems lists why the input is invalid, on a [CodeInvalidInput] error.
	Problems []string
}

// Error returns the status, code and message, followed by the problems
// when there are any.
func (e *APIError) Error() string {
	msg := fmt.Sprintf("dive: %d %s: %s", e.Status, e.Code, e.Message)
	if len(e.Problems) > 0 {
		msg += " (" + strings.Join(e.Problems, "; ") + ")"
	}
	return msg
}

// Is reports whether e is an idempotency violation with no run named, so
// that errors.Is(err, [ErrIdempotencyViolation]) matches it.
func (e *APIError) Is(target error) bool {
	return target == ErrIdempotencyViolation && e.Code == CodeIdempotencyViolation && e.RunID == ""
}

// AlreadyStartedError is returned by [Client.Start] when the start token
// already started a run. Treat it as success when retrying a start:
// watch Run with [Client.Entries] or [Client.Wait].
type AlreadyStartedError struct {
	// Run identifies the run the token already started. Its Status is empty.
	Run Run

	// Err is the [CodeIdempotencyViolation] response.
	Err *APIError
}

// Error names the run the token already started.
func (e *AlreadyStartedError) Error() string {
	return fmt.Sprintf("dive: the start token already started run %s", e.Run.ID)
}

// Unwrap returns e.Err.
func (e *AlreadyStartedError) Unwrap() error { return e.Err }

// AtCapacityError is returned by [Client] methods when dive serve answers
// with [CodeAtCapacity] because the process or the tenant is at its run
// limit. Retry after RetryAfter.
type AtCapacityError struct {
	// RetryAfter is how long to wait before retrying, from the Retry-After
	// header. It is zero when the header is missing or already past.
	RetryAfter time.Duration

	// Err is the [CodeAtCapacity] response.
	Err *APIError
}

// Error says that dive serve is at capacity, and when to retry if known.
func (e *AtCapacityError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("dive: at capacity; retry after %s", e.RetryAfter)
	}
	return "dive: at capacity"
}

// Unwrap returns e.Err.
func (e *AtCapacityError) Unwrap() error { return e.Err }

func readError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	api := &APIError{Status: resp.StatusCode}
	var body wire.ErrorBody
	if err := json.Unmarshal(raw, &body); err == nil && body.Error.Code != "" {
		api.Code = Code(body.Error.Code)
		api.Message = body.Error.Message
		api.Problems = body.Error.Problems
		if body.Error.RunID != nil {
			api.RunID = *body.Error.RunID
		}
		if body.Error.SessionID != nil {
			api.SessionID = *body.Error.SessionID
		}
	} else {
		api.Code = codeForStatus(resp.StatusCode)
		api.Message = strings.TrimSpace(string(raw))
		if api.Message == "" {
			api.Message = http.StatusText(resp.StatusCode)
		}
	}
	if api.Code == CodeAtCapacity {
		return &AtCapacityError{RetryAfter: retryAfter(resp.Header.Get("Retry-After")), Err: api}
	}
	return api
}

func codeForStatus(status int) Code {
	switch status {
	case http.StatusBadRequest:
		return CodeBadRequest
	case http.StatusUnauthorized:
		return CodeInvalidToken
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusConflict:
		return CodeIdempotencyViolation
	case http.StatusUnprocessableEntity:
		return CodeInvalidInput
	case http.StatusTooManyRequests:
		return CodeAtCapacity
	case http.StatusServiceUnavailable:
		return CodeShuttingDown
	}
	return CodeInternal
}

func retryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}
