package tools

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Call describes the tool call a tool function is serving. The handler
// from [Registry.Handler] builds it from the verified run token and the
// request headers, and the function reads it with [CallFrom].
type Call struct {
	// Tool is the name of the tool being called.
	Tool string

	// RunID is the id of the run making the call: the Dive-Run-Id header,
	// or the token's jti when the header is absent.
	RunID string

	// Tenant is the token's tenant claim.
	Tenant string

	// User is the token's sub claim.
	User string

	// Plan is the token's plan claim, the identifier of the plan the run
	// executes.
	Plan string

	// IdempotencyKey is the Idempotency-Key header. Dive sends the same key
	// when it retries a call, so a tool with side effects can use it to do
	// the work once.
	IdempotencyKey string

	// CallPath is the Dive-Call-Path header: where in the run the call was
	// made.
	CallPath string

	// Traceparent is the W3C traceparent header, or empty.
	Traceparent string

	// Claims holds the token's extra claims, as [token.Claims.Extra] does,
	// such as a role an app added with [token.StartClaims.Extra]. It is
	// never nil inside a call.
	Claims map[string]any

	// Dev reports whether the token was signed by the development key
	// that [token.Verifier.WithDev] trusts.
	Dev bool
}

type callKey struct{}

// WithCall returns a copy of ctx that carries call. The handler calls it
// before running a tool; use it to test a tool function directly.
func WithCall(ctx context.Context, call Call) context.Context {
	return context.WithValue(ctx, callKey{}, call)
}

// CallFrom returns the [Call] that ctx carries, or the zero Call when ctx
// carries none.
func CallFrom(ctx context.Context) Call {
	call, _ := ctx.Value(callKey{}).(Call)
	return call
}

// ToolError is an error a tool returns on purpose, made by [Errorf] or
// [Unavailable]. Its Message reaches the run, unlike the message of any
// other error. Use [errors.As] to inspect one.
type ToolError struct {
	// Message is the text sent in the error response.
	Message string

	// Unavailable reports whether the error is temporary. The handler
	// answers with 503 and code unavailable instead of 422 and code
	// refused.
	Unavailable bool

	// RetryAfter is how long Dive should wait before retrying an
	// Unavailable error. Zero means no Retry-After header is sent.
	RetryAfter time.Duration

	err error
}

// Error returns e.Message.
func (e *ToolError) Error() string { return e.Message }

// Unwrap returns the error formatted by [Errorf] or [Unavailable], so
// errors wrapped with %w stay reachable through [errors.Is] and
// [errors.As].
func (e *ToolError) Unwrap() error { return e.err }

// Errorf returns a [*ToolError] that refuses the call. The handler
// answers with 422 and the code refused, and the message, formatted as
// by [fmt.Errorf], reaches the run as the tool's error. Use it for
// errors the model can act on, such as a missing record or a permission
// the user lacks.
func Errorf(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	return &ToolError{Message: err.Error(), err: err}
}

// Unavailable returns a [*ToolError] for a temporary failure, such as a
// dependency that is down. The handler answers with 503 and the code
// unavailable, with the message formatted as by [fmt.Errorf], and Dive
// retries the call as the tool's [Retry] option allows. Add a delay with
// [RetryAfter].
func Unavailable(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	return &ToolError{Message: err.Error(), Unavailable: true, err: err}
}

// RetryAfter returns a copy of an error from [Unavailable] that asks Dive
// to wait d before retrying. The handler sends d in a Retry-After header,
// in whole seconds rounded up. RetryAfter returns err unchanged when err
// isn't an Unavailable error, so it is safe to call on any error.
func RetryAfter(err error, d time.Duration) error {
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || !toolErr.Unavailable {
		return err
	}
	later := *toolErr
	later.RetryAfter = d
	return &later
}
