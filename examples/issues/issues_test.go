package issues_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/loon-rocks/dive-go/examples/issues"
	"github.com/loon-rocks/dive-go/token"
	"github.com/loon-rocks/dive-go/tools"
)

func TestDiveToolFilesMatchCode(t *testing.T) {
	if err := issues.DiveTools(issues.NewStore(), nil).Check(".dive/tools"); err != nil {
		t.Fatal(err)
	}
}

type identity struct {
	tenant, user, role string
}

func devToken(t *testing.T, key ed25519.PrivateKey, who identity, runID string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss": "dive-dev", "aud": "dive", "sub": who.user, "tenant": who.tenant, "jti": runID,
		"plan": "file_issue", "dive_dev": true, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}
	if who.role != "" {
		claims["role"] = who.role
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestToolsActAsTheRunsIdentity(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	t.Setenv("DIVE_ENV", "")
	t.Setenv("DIVE_DEV_PUBLIC_KEY", string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := issues.DiveTools(issues.NewStore(), log).Handler(tools.HandlerOptions{
		Verifier: token.NewVerifier(nil).WithDev(token.DevKey()),
		Logger:   log,
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	call := func(tool, tok, runID, body string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/dive/tools/"+tool, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Dive-Run-Id", runID)
		req.Header.Set("Idempotency-Key", "dive-"+runID+"-"+tool)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}
	ada := devToken(t, priv, identity{"acme", "ada@example.com", "admin"}, "run-ada")
	status, created := call("create_issue", ada, "run-ada", `{"title":"Login fails"}`)
	if status != 200 {
		t.Fatalf("ada: %d %v", status, created)
	}
	again, retried := call("create_issue", ada, "run-ada", `{"title":"Login fails"}`)
	if again != 200 || retried["id"] != created["id"] {
		t.Fatalf("a retry with the same Idempotency-Key must return the same issue: %v %v", created, retried)
	}
	status, got := call("get_issue", ada, "run-ada", `{"id":"`+created["id"].(string)+`"}`)
	if status != 200 || got["tenant"] != "acme" || got["created_by"] != "ada@example.com" {
		t.Fatalf("get: %d %v", status, got)
	}
	viewer := devToken(t, priv, identity{"acme", "vic@example.com", "viewer"}, "run-vic")
	status, refused := call("create_issue", viewer, "run-vic", `{"title":"Login fails"}`)
	if status != http.StatusUnprocessableEntity || !strings.Contains(refused["error"].(map[string]any)["message"].(string), "viewer") {
		t.Fatalf("viewer: %d %v", status, refused)
	}
	globex := devToken(t, priv, identity{"globex", "hank@example.com", ""}, "run-hank")
	status, _ = call("get_issue", globex, "run-hank", `{"id":"`+created["id"].(string)+`"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("another tenant read the issue: %d", status)
	}
	for _, line := range []string{`"tenant":"acme"`, `"run_id":"run-ada"`, `"dev":true`} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("log lacks %s:\n%s", line, logs.String())
		}
	}
	for _, tok := range []string{ada, viewer, globex} {
		if strings.Contains(logs.String(), tok) {
			t.Fatal("a token reached the log")
		}
	}
}
