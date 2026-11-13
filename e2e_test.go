package dive_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dive "github.com/loon-rocks/dive-go"
	"github.com/loon-rocks/dive-go/token"
)

func droppingProxy(t *testing.T, upstream string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	var dropped atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/entries") || !dropped.CompareAndSwap(0, 1) {
			proxy.ServeHTTP(w, r)
			return
		}
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, upstream+r.URL.Path, nil)
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			_, _ = io.WriteString(w, line)
			if err != nil || line == "\n" {
				break
			}
		}
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(srv.Close)
	return srv, &dropped
}

func TestAgainstDiveServe(t *testing.T) {
	upstream := os.Getenv("DIVE_E2E_URL")
	if upstream == "" {
		t.Skip("set DIVE_E2E_URL to a dive serve --dev-key with a greet plan, and DIVE_DEV_KEY to its key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	signer, err := token.DevSigner()
	if err != nil {
		t.Fatal(err)
	}
	proxy, dropped := droppingProxy(t, upstream)
	client := dive.NewClient(proxy.URL)
	read, err := signer.Reader(token.ReadClaims{Tenant: "acme", User: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	plans, err := client.Plans(ctx, read)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) == 0 {
		t.Fatal("no plans")
	}
	start, runID, err := signer.Start(token.StartClaims{Plan: "greet", Tenant: "acme", User: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := client.Start(ctx, start, "greet", map[string]string{"name": "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != runID {
		t.Fatalf("run id %s, want the token's jti %s", run.ID, runID)
	}
	var seqs []uint64
	var last string
	for entry, err := range client.Entries(ctx, read, run.ID, dive.ReconnectDelay(10*time.Millisecond)) {
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, entry.Seq)
		last = entry.Kind
	}
	if dropped.Load() != 1 {
		t.Fatal("the proxy didn't drop the first stream")
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("seqs %v skip or repeat", seqs)
		}
	}
	if len(seqs) < 4 || last != dive.KindRunEnded {
		t.Fatalf("seqs %v, last kind %s", seqs, last)
	}
	view, err := client.Wait(ctx, read, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := dive.Result[struct {
		Greeting string `json:"greeting"`
	}](view)
	if err != nil || result.Greeting != "hello ada" {
		t.Fatalf("result %+v %v", result, err)
	}
	_, err = client.Start(ctx, start, "greet", map[string]string{"name": "ada"})
	var already *dive.AlreadyStartedError
	if !errors.As(err, &already) || already.Run.ID != run.ID {
		t.Fatalf("repeat start: %v", err)
	}
	cancelled, err := client.Cancel(ctx, read, run.ID)
	if err != nil || cancelled.Status != dive.StatusCompleted {
		t.Fatalf("cancel a finished run: %+v %v", cancelled, err)
	}
	_, err = client.Start(ctx, start, "greet", map[string]string{})
	if err == nil {
		t.Fatal("a reused token must not start a second run")
	}
	bad, _, _ := signer.Start(token.StartClaims{Plan: "greet", Tenant: "acme", User: "ada@example.com"})
	_, err = client.Start(ctx, bad, "greet", map[string]string{})
	var api *dive.APIError
	if !errors.As(err, &api) || api.Code != dive.CodeInvalidInput {
		t.Fatalf("missing input: %v", err)
	}
	_, err = client.Run(ctx, read, "no-such-run")
	if !errors.As(err, &api) || api.Code != dive.CodeNotFound {
		t.Fatalf("unknown run: %v", err)
	}
}
