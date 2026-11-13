package dive_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"

	dive "github.com/loon-rocks/dive-go"
	"github.com/loon-rocks/dive-go/token"
)

func fakeDiveServe() *httptest.Server {
	const run = "28e67bc9-d9e7-4995-ab9e-fbb663d6592b"
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/plans/greet/runs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"run_id":%q,"session_id":"349d2ec8f590","status":"running","entries_url":"/v1/runs/%s/entries"}`, run, run)
	})
	mux.HandleFunc("GET /v1/runs/{run}/entries", func(w http.ResponseWriter, _ *http.Request) {
		data, err := os.ReadFile("testdata/greet.sse")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /v1/runs/{run}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q,"session_id":"349d2ec8f590","plan":"greet","status":"completed","outcome":"ok","result":{"greeting":"hello ada"},"first_seq":1,"last_seq":4,"cancel_requested":false,"started_at":"2026-11-13T04:21:25Z","ended_at":"2026-11-13T04:21:26Z"}`, run)
	})
	return httptest.NewServer(mux)
}

func Example() {
	serve := fakeDiveServe()
	defer serve.Close()
	_, appKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}

	signer, err := token.NewSigner(appKey, token.Issuer("https://app.example.com"))
	if err != nil {
		log.Fatal(err)
	}
	start, _, err := signer.Start(token.StartClaims{Plan: "greet", Tenant: "acme", User: "ada@example.com"})
	if err != nil {
		log.Fatal(err)
	}
	read, err := signer.Reader(token.ReadClaims{Tenant: "acme", User: "ada@example.com"})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	client := dive.NewClient(serve.URL)
	run, err := client.Start(ctx, start, "greet", map[string]any{"name": "ada"})
	if err != nil {
		log.Fatal(err)
	}
	for entry, err := range client.Entries(ctx, read, run.ID) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(entry.Seq, entry.Kind)
	}

	view, err := client.Wait(ctx, read, run.ID)
	if err != nil {
		log.Fatal(err)
	}
	result, err := dive.Result[struct {
		Greeting string `json:"greeting"`
	}](view)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(view.Status, result.Greeting)
	// Output:
	// 1 run_started
	// 2 step_started
	// 3 step_finished
	// 4 run_ended
	// completed hello ada
}
