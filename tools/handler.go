package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/loon-rocks/dive-go/token"
)

// DefaultMaxBodyBytes is the largest request body, 4 MiB, that the
// handler from [Registry.Handler] reads when [HandlerOptions.MaxBodyBytes]
// is zero.
const DefaultMaxBodyBytes = 4 << 20

const toolFailed = "the tool failed; the app's log has the details"

const (
	codeRefused      = "refused"
	codeInvalidInput = "invalid_input"
	codeUnavailable  = "unavailable"
	codeTimeout      = "timeout"
	codeInternal     = "internal"
	codeUnauthorized = "unauthorized"
	codeUnknownTool  = "unknown_tool"
)

var errToolTimeout = errors.New("the tool's timeout passed")

// TokenVerifier verifies the run token of a tool call. [*token.Verifier]
// implements it.
type TokenVerifier interface {
	// Verify returns the claims of raw, the token without its "Bearer "
	// prefix, or an error when the token isn't valid. The error message
	// is sent in the 401 response.
	Verify(raw string) (*token.Claims, error)
}

// HandlerOptions configures the handler that [Registry.Handler] returns.
// Only Verifier is required.
type HandlerOptions struct {
	// Verifier verifies the run token of every call. Handler panics when
	// it is nil.
	Verifier TokenVerifier

	// RunOpen, when set, is called with the run id, the token's jti, after
	// the token verifies. When it returns false, the call is refused with 401, so an
	// app can reject calls from runs it has closed or never started. Nil
	// means every run is open.
	RunOpen func(ctx context.Context, runID string) bool

	// Wrap, when set, wraps the whole handler, for middleware such as
	// tracing or metrics. Nil means no wrapping.
	Wrap func(http.Handler) http.Handler

	// Logger receives the real error of every 500 response, with its ref,
	// and a warning for every timeout. Nil means [slog.Default].
	Logger *slog.Logger

	// MaxBodyBytes limits the size of a call's input. A larger body is
	// refused with 413. Zero or less means [DefaultMaxBodyBytes].
	MaxBodyBytes int64
}

type handler struct {
	registry *Registry
	opts     HandlerOptions
}

// Handler returns an [http.Handler] that serves the tools in r to Dive.
// Mount it under the path of the registry's base URL, such as
// "/dive/tools/": it takes the tool's name from the last segment of the
// request path, so a call to POST /dive/tools/create_issue runs the tool
// named create_issue. Tools added to r after Handler is called are served
// too.
//
// For each request the handler checks, in order, that the method is POST,
// that the Authorization header holds a run token that
// [HandlerOptions.Verifier] accepts and that has a jti, that the
// Dive-Run-Id header, when present, matches the jti, that [HandlerOptions.RunOpen] allows
// the run, that the tool exists, and that the body is JSON that matches
// the tool's input schema and decodes into its input type with no unknown
// fields. It then calls the tool function with a context that carries the
// [Call] and, when the tool has a [Timeout], that deadline.
//
// A tool that returns normally gets a 200 response with its output as
// JSON. Every other response has the body
//
//	{"error": {"code": "...", "message": "..."}}
//
// with one of these statuses and codes:
//
//   - 400 invalid_input: the body isn't JSON, doesn't match the input
//     schema, or doesn't decode into the input type. 413 invalid_input:
//     the body is larger than [HandlerOptions.MaxBodyBytes].
//   - 401 unauthorized: the token is missing, doesn't verify or has no
//     jti (a reader token from [token.Signer.Reader]), belongs to another
//     run, or RunOpen returned false.
//   - 404 unknown_tool: no tool has the name. 405 unknown_tool: the
//     method isn't POST.
//   - 422 refused: the tool returned an error from [Errorf].
//   - 503 unavailable: the tool returned an error from [Unavailable],
//     with a Retry-After header when [RetryAfter] set one.
//   - 504 timeout: the tool's [Timeout] passed before it returned,
//     whatever it returned.
//   - 500 internal: the tool returned any other error, panicked, or
//     returned output that doesn't encode as JSON. The message is
//     generic, and the body has a "ref" that is logged to
//     [HandlerOptions.Logger] beside the real error, which never reaches
//     the response.
//
// Handler panics when opts.Verifier is nil.
func (r *Registry) Handler(opts HandlerOptions) http.Handler {
	if opts.Verifier == nil {
		panic("tools: HandlerOptions.Verifier is required; every tool call carries a run token")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	var h http.Handler = &handler{registry: r, opts: opts}
	if opts.Wrap != nil {
		h = opts.Wrap(h)
	}
	return h
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeBody(w, status, map[string]string{"code": code, "message": message})
}

func writeBody(w http.ResponseWriter, status int, fields map[string]string) {
	body, _ := json.Marshal(map[string]any{"error": fields})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func bearer(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, codeUnknownTool, "tools are called with POST")
		return
	}
	raw := bearer(r)
	if raw == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "the call has no run token; send Authorization: Bearer <run token>")
		return
	}
	claims, err := h.opts.Verifier.Verify(raw)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeError(w, http.StatusUnauthorized, codeUnauthorized, err.Error())
		return
	}
	runID := claims.ID
	if runID == "" {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "the token has no jti, so it doesn't belong to a run; tools take the run's start token")
		return
	}
	if header := r.Header.Get("Dive-Run-Id"); header != "" && header != runID {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "the run token belongs to another run")
		return
	}
	if h.opts.RunOpen != nil && !h.opts.RunOpen(r.Context(), runID) {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "the run is closed")
		return
	}
	t := h.registry.lookup(name)
	if t == nil {
		writeError(w, http.StatusNotFound, codeUnknownTool, fmt.Sprintf("no tool %q", name))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.opts.MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codeInvalidInput, fmt.Sprintf("the input is larger than %d bytes", h.opts.MaxBodyBytes))
			return
		}
		writeError(w, http.StatusBadRequest, codeInvalidInput, "couldn't read the input")
		return
	}
	var instance any
	if err := json.Unmarshal(body, &instance); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidInput, "the input isn't JSON")
		return
	}
	if err := t.resolved.Validate(instance); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidInput, "the input doesn't match the tool's input schema: "+err.Error())
		return
	}
	extra := make(map[string]any, len(claims.Extra))
	for k, v := range claims.Extra {
		extra[k] = v
	}
	call := Call{
		Tool:           name,
		RunID:          runID,
		Tenant:         claims.Tenant,
		User:           claims.Subject,
		Plan:           claims.Plan,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		CallPath:       r.Header.Get("Dive-Call-Path"),
		Traceparent:    r.Header.Get("traceparent"),
		Claims:         extra,
		Dev:            claims.Dev,
	}
	ctx := WithCall(r.Context(), call)
	if t.settings.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, t.settings.timeout, errToolTimeout)
		defer cancel()
	}
	h.invoke(ctx, w, t, call, body)
}

func (h *handler) invoke(ctx context.Context, w http.ResponseWriter, t *tool, call Call, body []byte) {
	attrs := []any{"tool", call.Tool, "run_id", call.RunID, "tenant", call.Tenant, "call_path", call.CallPath}
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if recovered == http.ErrAbortHandler {
			panic(recovered)
		}
		h.internal(ctx, w, "dive tool panicked", append(attrs, "panic", fmt.Sprint(recovered)))
	}()
	out, err := t.invoke(ctx, body)
	if context.Cause(ctx) == errToolTimeout {
		if err != nil {
			attrs = append(attrs, "error", err.Error())
		}
		h.opts.Logger.WarnContext(ctx, "dive tool timed out", attrs...)
		writeError(w, http.StatusGatewayTimeout, codeTimeout, fmt.Sprintf("the tool didn't finish within %s", t.settings.timeout))
		return
	}
	if err != nil {
		var bad *inputError
		var toolErr *ToolError
		switch {
		case errors.As(err, &bad):
			writeError(w, http.StatusBadRequest, codeInvalidInput, "the input doesn't fit the tool: "+bad.Error())
		case errors.As(err, &toolErr) && toolErr.Unavailable:
			if toolErr.RetryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(int((toolErr.RetryAfter+time.Second-1)/time.Second)))
			}
			writeError(w, http.StatusServiceUnavailable, codeUnavailable, toolErr.Message)
		case toolErr != nil:
			writeError(w, http.StatusUnprocessableEntity, codeRefused, toolErr.Message)
		default:
			h.internal(ctx, w, "dive tool failed", append(attrs, "error", err.Error()))
		}
		return
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		h.internal(ctx, w, "dive tool output didn't encode", append(attrs, "error", err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func (h *handler) internal(ctx context.Context, w http.ResponseWriter, msg string, attrs []any) {
	ref := newRef()
	h.opts.Logger.ErrorContext(ctx, msg, append(attrs, "ref", ref)...)
	writeBody(w, http.StatusInternalServerError, map[string]string{"code": codeInternal, "message": toolFailed, "ref": ref})
}

func newRef() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
