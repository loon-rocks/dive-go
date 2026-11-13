package tools_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/loon-rocks/dive-go/token"
	"github.com/loon-rocks/dive-go/tools"
)

type CreateIssueInput struct {
	Title    string `json:"title" jsonschema:"One-line summary"`
	Priority string `json:"priority,omitempty"`
}

type CreateIssueOutput struct {
	ID     string `json:"id"`
	Tenant string `json:"tenant"`
	User   string `json:"user"`
	Role   string `json:"role,omitempty"`
}

var errBoom = errors.New("database password is hunter2")

type fixture struct {
	server *httptest.Server
	signer *token.Signer
	logs   *bytes.Buffer
	seen   chan tools.Call
	open   atomic.Bool
}

func newFixture(t *testing.T, edit func(*tools.HandlerOptions)) *fixture {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := token.NewSigner(priv, token.Issuer("https://app.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{signer: signer, logs: &bytes.Buffer{}, seen: make(chan tools.Call, 16)}
	f.open.Store(true)
	r := tools.New()
	tools.Add(r, tools.Def{Name: "create_issue", Description: "Creates an issue."}, func(ctx context.Context, in CreateIssueInput) (CreateIssueOutput, error) {
		call := tools.CallFrom(ctx)
		f.seen <- call
		switch in.Title {
		case "read-only":
			return CreateIssueOutput{}, tools.Errorf("the tracker is read-only for %s", call.Tenant)
		case "boom":
			return CreateIssueOutput{}, errBoom
		case "down":
			return CreateIssueOutput{}, tools.Unavailable("the tracker is in maintenance")
		case "busy":
			return CreateIssueOutput{}, tools.RetryAfter(tools.Unavailable("the board is locked"), 1500*time.Millisecond)
		case "panic":
			panic("kaboom")
		}
		role, _ := call.Claims["role"].(string)
		return CreateIssueOutput{ID: "ISS-1", Tenant: call.Tenant, User: call.User, Role: role}, nil
	}, tools.Schema(func(in, _ *jsonschema.Schema) {
		in.Properties["priority"].Enum = []any{"low", "normal", "urgent"}
	}))
	tools.Add(r, tools.Def{Name: "slow_lookup", Description: "Waits for its deadline."}, func(ctx context.Context, in CreateIssueInput) (CreateIssueOutput, error) {
		<-ctx.Done()
		switch in.Title {
		case "ok":
			return CreateIssueOutput{ID: "late"}, nil
		case "refuse":
			return CreateIssueOutput{}, tools.Errorf("too late")
		}
		return CreateIssueOutput{}, ctx.Err()
	}, tools.Timeout(time.Second))
	opts := tools.HandlerOptions{
		Verifier: token.NewVerifier(pub, token.Issuer("https://app.example.com")),
		Logger:   slog.New(slog.NewTextHandler(f.logs, nil)),
		RunOpen:  func(context.Context, string) bool { return f.open.Load() },
	}
	if edit != nil {
		edit(&opts)
	}
	mux := http.NewServeMux()
	mux.Handle("/dive/tools/", r.Handler(opts))
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixture) start(t *testing.T) (token.Token, string) {
	t.Helper()
	tok, runID, err := f.signer.Start(token.StartClaims{Plan: "file_issue", Tenant: "acme", User: "ada@example.com", Extra: map[string]any{"role": "admin"}})
	if err != nil {
		t.Fatal(err)
	}
	return tok, runID
}

type response struct {
	status int
	body   map[string]any
	raw    string
	header http.Header
}

func (f *fixture) post(t *testing.T, tool, body string, headers map[string]string) response {
	t.Helper()
	return f.do(t, http.MethodPost, tool, body, headers)
}

func (f *fixture) do(t *testing.T, method, tool, body string, headers map[string]string) response {
	t.Helper()
	req, err := http.NewRequest(method, f.server.URL+"/dive/tools/"+tool, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := response{status: resp.StatusCode, raw: string(raw), header: resp.Header}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func runHeaders(tok token.Token, runID string) map[string]string {
	return map[string]string{
		"Authorization":   "Bearer " + string(tok),
		"Idempotency-Key": "dive-abc123",
		"Dive-Run-Id":     runID,
		"Dive-Call-Path":  "plan:file_issue/step:E1",
		"traceparent":     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"User-Agent":      "dive/0.20.0",
	}
}

func errorField(t *testing.T, r response, field string) string {
	t.Helper()
	e, ok := r.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error body in %q", r.raw)
	}
	value, _ := e[field].(string)
	return value
}

func errorMessage(t *testing.T, r response) string {
	t.Helper()
	return errorField(t, r, "message")
}

func errorCode(t *testing.T, r response) string {
	t.Helper()
	return errorField(t, r, "code")
}

func TestHandlerCallsTheToolWithTheRunsIdentity(t *testing.T) {
	f := newFixture(t, nil)
	tok, runID := f.start(t)
	r := f.post(t, "create_issue", `{"title":"Login fails","priority":"urgent"}`, runHeaders(tok, runID))
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.raw)
	}
	if r.body["tenant"] != "acme" || r.body["user"] != "ada@example.com" || r.body["role"] != "admin" {
		t.Fatalf("body %v", r.body)
	}
	call := <-f.seen
	want := tools.Call{
		Tool: "create_issue", RunID: runID, Tenant: "acme", User: "ada@example.com", Plan: "file_issue",
		IdempotencyKey: "dive-abc123", CallPath: "plan:file_issue/step:E1",
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
	call.Claims = nil
	if !reflect.DeepEqual(call, want) {
		t.Fatalf("call %+v\nwant %+v", call, want)
	}
}

func TestHandlerRefusesMissingAndBadTokens(t *testing.T) {
	f := newFixture(t, nil)
	tok, runID := f.start(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	forged, _ := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"iss": "https://app.example.com", "aud": "dive", "sub": "x", "tenant": "acme", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(other)
	cases := map[string]map[string]string{
		"no header":    {},
		"not bearer":   {"Authorization": "Basic Zm9vOmJhcg=="},
		"empty bearer": {"Authorization": "Bearer "},
		"forged":       {"Authorization": "Bearer " + forged},
		"garbage":      {"Authorization": "Bearer abc.def.ghi"},
		"another run":  {"Authorization": "Bearer " + string(tok), "Dive-Run-Id": "someone-else"},
	}
	for name, headers := range cases {
		r := f.post(t, "create_issue", `{"title":"x"}`, headers)
		if r.status != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, r.status)
		}
		if errorMessage(t, r) == "" {
			t.Errorf("%s: no message", name)
		}
		if errorCode(t, r) != "unauthorized" {
			t.Errorf("%s: code %q", name, errorCode(t, r))
		}
	}
	reader, err := f.signer.Reader(token.ReadClaims{Tenant: "acme", User: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	r := f.post(t, "create_issue", `{"title":"x"}`, map[string]string{"Authorization": "Bearer " + string(reader), "Dive-Run-Id": runID})
	if r.status != http.StatusUnauthorized || !strings.Contains(errorMessage(t, r), "no jti") {
		t.Errorf("reader token: %d %s", r.status, r.raw)
	}
	if r := f.post(t, "create_issue", `{"title":"x"}`, map[string]string{"Authorization": "Bearer " + string(tok)}); r.status != http.StatusOK {
		t.Errorf("start token without Dive-Run-Id: %d %s", r.status, r.raw)
	}
	if call := <-f.seen; call.RunID != runID {
		t.Errorf("run id %q", call.RunID)
	}
	f.open.Store(false)
	if r := f.post(t, "create_issue", `{"title":"x"}`, runHeaders(tok, runID)); r.status != http.StatusUnauthorized {
		t.Errorf("closed run: status %d", r.status)
	}
	select {
	case <-f.seen:
		t.Fatal("the tool ran without a valid token")
	default:
	}
}

func TestHandlerRejectsBadInput(t *testing.T) {
	f := newFixture(t, nil)
	tok, runID := f.start(t)
	cases := map[string]string{
		"not json":      `{"title":`,
		"unknown field": `{"title":"x","severity":"high"}`,
		"missing title": `{"priority":"low"}`,
		"wrong type":    `{"title":3}`,
		"not in enum":   `{"title":"x","priority":"whenever"}`,
		"not object":    `["x"]`,
		"empty":         ``,
	}
	for name, body := range cases {
		r := f.post(t, "create_issue", body, runHeaders(tok, runID))
		if r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_input" {
			t.Errorf("%s: status %d %s", name, r.status, r.raw)
		}
	}
	if r := f.post(t, "nope", `{}`, runHeaders(tok, runID)); r.status != http.StatusNotFound || errorCode(t, r) != "unknown_tool" {
		t.Errorf("unknown tool: %d %s", r.status, r.raw)
	}
	if r := f.do(t, http.MethodGet, "create_issue", "", runHeaders(tok, runID)); r.status != http.StatusMethodNotAllowed || r.header.Get("Allow") != "POST" {
		t.Errorf("GET: %d", r.status)
	}
}

func TestHandlerMapsToolErrors(t *testing.T) {
	f := newFixture(t, nil)
	tok, runID := f.start(t)
	r := f.post(t, "create_issue", `{"title":"read-only"}`, runHeaders(tok, runID))
	if r.status != http.StatusUnprocessableEntity || errorCode(t, r) != "refused" || errorMessage(t, r) != "the tracker is read-only for acme" {
		t.Fatalf("Errorf: %d %s", r.status, r.raw)
	}
	r = f.post(t, "create_issue", `{"title":"boom"}`, runHeaders(tok, runID))
	if r.status != http.StatusInternalServerError || errorCode(t, r) != "internal" || strings.Contains(r.raw, "hunter2") {
		t.Fatalf("plain error: %d %s", r.status, r.raw)
	}
	ref := errorField(t, r, "ref")
	if len(ref) != 8 || !strings.Contains(f.logs.String(), "ref="+ref) {
		t.Fatalf("ref %q isn't in the log:\n%s", ref, f.logs.String())
	}
	r = f.post(t, "create_issue", `{"title":"panic"}`, runHeaders(tok, runID))
	if r.status != http.StatusInternalServerError || strings.Contains(r.raw, "kaboom") {
		t.Fatalf("panic: %d %s", r.status, r.raw)
	}
	logs := f.logs.String()
	for _, want := range []string{"hunter2", "kaboom", runID, "tenant=acme"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, string(tok)) {
		t.Fatal("the token reached the log")
	}
}

func TestHandlerAnswersUnavailableAndTimeout(t *testing.T) {
	f := newFixture(t, nil)
	tok, runID := f.start(t)
	r := f.post(t, "create_issue", `{"title":"down"}`, runHeaders(tok, runID))
	if r.status != http.StatusServiceUnavailable || errorCode(t, r) != "unavailable" || errorMessage(t, r) != "the tracker is in maintenance" || r.header.Get("Retry-After") != "" {
		t.Fatalf("Unavailable: %d %v %s", r.status, r.header, r.raw)
	}
	r = f.post(t, "create_issue", `{"title":"busy"}`, runHeaders(tok, runID))
	if r.status != http.StatusServiceUnavailable || r.header.Get("Retry-After") != "2" {
		t.Fatalf("RetryAfter: %d %v %s", r.status, r.header, r.raw)
	}
	for _, title := range []string{"x", "ok", "refuse"} {
		r = f.post(t, "slow_lookup", `{"title":"`+title+`"}`, runHeaders(tok, runID))
		if r.status != http.StatusGatewayTimeout || errorCode(t, r) != "timeout" || errorMessage(t, r) != "the tool didn't finish within 1s" {
			t.Fatalf("timeout returning %s: %d %s", title, r.status, r.raw)
		}
	}
}

func TestRetryAfterLeavesOtherErrorsAlone(t *testing.T) {
	refused := tools.Errorf("no")
	if got := tools.RetryAfter(refused, time.Second); got != refused {
		t.Fatalf("got %v", got)
	}
	if got := tools.RetryAfter(errBoom, time.Second); got != errBoom {
		t.Fatalf("got %v", got)
	}
}

func TestErrorfKeepsEveryWrappedError(t *testing.T) {
	other := errors.New("second")
	err := tools.Errorf("%w and %w", errBoom, other)
	if !errors.Is(err, errBoom) || !errors.Is(err, other) {
		t.Fatalf("lost a wrapped error: %v", err)
	}
}

func TestErrorfWraps(t *testing.T) {
	err := tools.Errorf("no issue %s: %w", "ENG-1", errBoom)
	if !errors.Is(err, errBoom) {
		t.Fatal("Errorf should wrap with %w")
	}
	var te *tools.ToolError
	if !errors.As(err, &te) || te.Message != "no issue ENG-1: "+errBoom.Error() {
		t.Fatalf("got %v", err)
	}
}

func TestHandlerWrapAndDevTokens(t *testing.T) {
	devPub, devPriv, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv("DIVE_ENV", "")
	t.Setenv("DIVE_DEV_PUBLIC_KEY", pemPublic(t, devPub))
	var wrapped atomic.Int32
	f := newFixture(t, func(o *tools.HandlerOptions) {
		o.Verifier = o.Verifier.(*token.Verifier).WithDev(token.DevKey())
		o.Wrap = func(h http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wrapped.Add(1)
				h.ServeHTTP(w, r)
			})
		}
	})
	devToken, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"iss": "dive-dev", "aud": "dive", "sub": "vic@example.com", "tenant": "acme", "jti": "run-dev-1",
		"plan": "file_issue", "role": "viewer", "dive_dev": true,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(devPriv)
	if err != nil {
		t.Fatal(err)
	}
	r := f.post(t, "create_issue", `{"title":"x"}`, runHeaders(token.Token(devToken), "run-dev-1"))
	if r.status != http.StatusOK || r.body["role"] != "viewer" || r.body["user"] != "vic@example.com" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	call := <-f.seen
	if !call.Dev || call.RunID != "run-dev-1" {
		t.Fatalf("call %+v", call)
	}
	if wrapped.Load() != 1 {
		t.Fatal("Wrap wasn't applied")
	}
}

func TestHandlerNeedsAVerifier(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	tools.New().Handler(tools.HandlerOptions{})
}

func pemPublic(t *testing.T, key ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}
