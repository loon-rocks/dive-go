package tools_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/loon-rocks/dive-go/token"
	"github.com/loon-rocks/dive-go/tools"
)

type greetInput struct {
	Name string `json:"name" jsonschema:"Who to greet"`
}

type greetOutput struct {
	Greeting string `json:"greeting"`
}

type lookupInput struct {
	ID string `json:"id"`
}

type lookupOutput struct {
	Title string `json:"title"`
}

func lookupTools() *tools.Registry {
	r := tools.New()
	tools.Add(r, tools.Def{
		Name:        "lookup",
		Description: "Reads a record by id.",
	}, func(ctx context.Context, in lookupInput) (lookupOutput, error) {
		switch in.ID {
		case "missing":
			return lookupOutput{}, tools.Errorf("no record %s in %s", in.ID, tools.CallFrom(ctx).Tenant)
		case "busy":
			return lookupOutput{}, tools.RetryAfter(tools.Unavailable("the records service is busy"), 1500*time.Millisecond)
		}
		return lookupOutput{Title: "Record " + in.ID}, nil
	}, tools.ReadOnly(), tools.Retry(3))
	return r
}

func call(h http.Handler, signer *token.Signer, tool, body string) {
	tok, _, err := signer.Start(token.StartClaims{Plan: "triage", Tenant: "acme", User: "ada@example.com"})
	if err != nil {
		log.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dive/tools/"+tool, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+string(tok))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Result().Body)
	if retry := rec.Header().Get("Retry-After"); retry != "" {
		fmt.Println("Retry-After:", retry)
	}
	fmt.Println(rec.Code, strings.TrimSpace(string(out)))
}

func ExampleAdd() {
	r := tools.New()
	tools.Add(r, tools.Def{
		Name:        "greet",
		Description: "Greets someone by name.",
	}, func(ctx context.Context, in greetInput) (greetOutput, error) {
		return greetOutput{Greeting: "hello " + in.Name}, nil
	}, tools.ReadOnly(), tools.Timeout(10*time.Second))

	files, err := r.Files()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(r.Names())
	fmt.Print(string(files["greet.yaml"]))
	// Output:
	// [greet]
	// version: 5
	// name: greet
	// description: Greets someone by name.
	// kind: http
	// generated: dive-go
	// method: POST
	// url: ${APP_URL}/dive/tools/greet
	// read_only: true
	// timeout_secs: 10
	// input_schema:
	//   additionalProperties: false
	//   properties:
	//     name:
	//       description: Who to greet
	//       type: string
	//   required:
	//     - name
	//   type: object
	// output_schema:
	//   additionalProperties: false
	//   properties:
	//     greeting:
	//       type: string
	//   required:
	//     - greeting
	//   type: object
}

func ExampleRegistry_Handler() {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	signer, err := token.NewSigner(private, token.Issuer("https://app.example.com"))
	if err != nil {
		log.Fatal(err)
	}

	verifier := token.NewVerifier(public, token.Issuer("https://app.example.com"))
	handler := lookupTools().Handler(tools.HandlerOptions{
		Verifier: verifier,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	mux := http.NewServeMux()
	mux.Handle("/dive/tools/", handler)

	call(mux, signer, "lookup", `{"id":"7"}`)
	call(mux, signer, "lookup", `{"id":7}`)
	call(mux, signer, "nope", `{}`)
	// Output:
	// 200 {"title":"Record 7"}
	// 400 {"error":{"code":"invalid_input","message":"the input doesn't match the tool's input schema: validating root: validating /properties/id: type: 7 has type \"integer\", want \"string\""}}
	// 404 {"error":{"code":"unknown_tool","message":"no tool \"nope\""}}
}

func ExampleErrorf() {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := token.NewSigner(private, token.Issuer("https://app.example.com"))
	handler := lookupTools().Handler(tools.HandlerOptions{
		Verifier: token.NewVerifier(private.Public(), token.Issuer("https://app.example.com")),
	})

	call(handler, signer, "lookup", `{"id":"missing"}`)
	// Output:
	// 422 {"error":{"code":"refused","message":"no record missing in acme"}}
}

func ExampleUnavailable() {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := token.NewSigner(private, token.Issuer("https://app.example.com"))
	handler := lookupTools().Handler(tools.HandlerOptions{
		Verifier: token.NewVerifier(private.Public(), token.Issuer("https://app.example.com")),
	})

	call(handler, signer, "lookup", `{"id":"busy"}`)
	// Output:
	// Retry-After: 2
	// 503 {"error":{"code":"unavailable","message":"the records service is busy"}}
}

func ExampleCallFrom() {
	greet := func(ctx context.Context, in greetInput) (greetOutput, error) {
		call := tools.CallFrom(ctx)
		if role, _ := call.Claims["role"].(string); role == "viewer" {
			return greetOutput{}, tools.Errorf("%s can't greet anyone in %s", call.User, call.Tenant)
		}
		return greetOutput{Greeting: "hello " + in.Name + " from " + call.Tenant}, nil
	}

	ctx := tools.WithCall(context.Background(), tools.Call{
		Tool:   "greet",
		Tenant: "acme",
		User:   "ada@example.com",
		Claims: map[string]any{"role": "viewer"},
	})
	_, err := greet(ctx, greetInput{Name: "grace"})
	fmt.Println(err)
	// Output:
	// ada@example.com can't greet anyone in acme
}
