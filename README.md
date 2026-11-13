# dive-go

Go SDK for [Dive](https://loon.rocks) as a service. Define Dive tools as Go functions, serve them over HTTP, and start, watch and cancel plan runs on `dive serve`.

```bash
go get github.com/loon-rocks/dive-go
```

Go 1.23 or later.

| Package | For |
| --- | --- |
| `github.com/loon-rocks/dive-go/tools` | Defining tools, generating their `.dive/tools/` files, and serving them |
| `github.com/loon-rocks/dive-go/token` | Minting and verifying run tokens |
| `github.com/loon-rocks/dive-go` (package `dive`) | The `dive serve` client |
| `cmd/dive-go-plans` | Generates typed start wrappers from `.dive/plans/*.yaml` |

`examples/issues` is a complete app: two tools, one plan, the generator, the drift test and a small server.

## Define tools

```go
type CreateIssueInput struct {
	Title    string `json:"title" jsonschema:"One-line summary of the problem"`
	Priority string `json:"priority,omitempty" jsonschema:"low, normal or urgent"`
}

type CreateIssueOutput struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func DiveTools(store *Store) *tools.Registry {
	r := tools.New()
	tools.Add(r, tools.Def{
		Name:        "create_issue",
		Description: "Creates an issue in the tenant's tracker and returns its id and URL.",
	}, func(ctx context.Context, in CreateIssueInput) (CreateIssueOutput, error) {
		call := tools.CallFrom(ctx)
		if call.Claims["role"] == "viewer" {
			return CreateIssueOutput{}, tools.Errorf("viewers can't create issues")
		}
		issue := store.Create(call.Tenant, call.User, in.Title, call.IdempotencyKey)
		return CreateIssueOutput{ID: issue.ID, URL: issue.URL}, nil
	}, tools.Retry(3), tools.Schema(func(in, out *jsonschema.Schema) {
		in.Properties["priority"].Enum = []any{"low", "normal", "urgent"}
	}))
	return r
}
```

- Schemas are reflected with [google/jsonschema-go](https://github.com/google/jsonschema-go): `json` tags name properties, a field without `omitempty` is required, and the `jsonschema` tag is the description. Use `tools.Schema` for anything tags can't say, such as enums.
- Options: `tools.ReadOnly()`, `tools.Retry(attempts)`, `tools.Timeout(d)` and `tools.Schema(fn)`.
- `tools.CallFrom(ctx)` returns the run id, tenant, user, plan, extra claims, idempotency key, call path and `traceparent`, and `Dev` for a token signed by the local dev key.
- Errors answer with `{"error":{"code","message"}}`, and Dive tells the model which kind of failure it was:

  | The tool | Status | `code` | Dive retries |
  | --- | --- | --- | --- |
  | returns `tools.Errorf(...)` | 422 | `refused` | no |
  | returns `tools.Unavailable(...)`, optionally through `tools.RetryAfter(err, d)` | 503 | `unavailable` | yes, per `tools.Retry` |
  | runs past `tools.Timeout(d)` | 504 | `timeout` | yes, per `tools.Retry` |
  | returns any other error, or panics | 500 | `internal`, with a `ref` | never |

  A 500 carries only a generic message and a short `ref`; the real error goes to `HandlerOptions.Logger` under the same `ref`.
- Tool names are lowercase letters, digits and `_`. Dive calls them `user__<name>`.

## Generate the tool files

Add a generator program and a test:

```go
//go:generate go run ./cmd/divetools

package main

func main() {
	if err := app.DiveTools(nil).Generate(".dive/tools"); err != nil {
		log.Fatal(err)
	}
}
```

```go
func TestDiveToolFilesMatchCode(t *testing.T) {
	if err := app.DiveTools(nil).Check("../../.dive/tools"); err != nil {
		t.Fatal(err)
	}
}
```

`Generate` writes one `kind: http` file per tool, marked `generated: dive-go`, with the same bytes on every run. It removes generated files for tools no longer in code and refuses to overwrite a file it didn't generate. `Check` fails with a per-file diff when the directory and the code disagree, so CI catches a forgotten `go generate`.

The URL in each file is `${APP_URL}/dive/tools/<name>`. Set `APP_URL` beside `dive serve`, or change the base with `tools.New(tools.BaseURL("..."))`.

## Serve the tools

```go
verifier := token.NewVerifier(appPublicKey, token.Issuer("https://app.example.com"))
if os.Getenv("DIVE_ENV") != "production" {
	verifier = verifier.WithDev(token.DevKey())
}

mux.Handle("/dive/tools/", app.DiveTools(store).Handler(tools.HandlerOptions{
	Verifier: verifier,
	RunOpen:  func(ctx context.Context, runID string) bool { return runs.Open(ctx, runID) },
	Wrap:     func(h http.Handler) http.Handler { return otelhttp.NewHandler(h, "dive-tools") },
}))
```

The handler serves `POST /dive/tools/{name}`. Every call needs the run's start token: none, one that doesn't verify, a reader token (no `jti`) or a `Dive-Run-Id` that isn't the token's `jti` is a 401 `unauthorized`. An unknown tool is a 404 `unknown_tool`. The body must match the tool's input schema, with no unknown fields, or it's a 400 `invalid_input`. A `NewVerifier` that trusts a key needs `token.Issuer`.

## Run tokens

```go
signer, err := token.NewSigner(privateKey, token.Issuer("https://app.example.com"), token.Audience("dive"))

start, runID, err := signer.Start(token.StartClaims{Plan: "triage", Tenant: org.ID, User: user.Email, TTL: time.Hour})
read, err := signer.Reader(token.ReadClaims{Tenant: org.ID, User: user.Email})
```

- Keys: Ed25519 (EdDSA), P-256 (ES256) or RSA (RS256). `token.ParsePrivateKeyPEM` and `token.ParsePublicKeyPEM` read PEM files.
- A start token carries `iss`, `aud`, `sub`, `tenant`, `iat`, `exp`, `plan` and a fresh UUID `jti`, which becomes the run's id. `StartClaims.Extra` adds top-level claims, such as a role.
- A read token carries `scope`, by default `plans:read runs:read runs:cancel`.
- TTL defaults to one hour. Set a start token's TTL above your longest plan.
- `token.Token` redacts itself in `fmt`, `slog` and JSON. Use `string(tok)` only where the raw value must go.

### Development

`dive serve --dev-key`, desktop and the CLI sign tokens with a local Ed25519 key at `~/.local/share/dive/serve/dev-key.pem`.

| Function | Reads | Use |
| --- | --- | --- |
| `token.DevKey()` | `DIVE_DEV_PUBLIC_KEY` (PEM text or a path), else `dev-key.pub.pem`, else the public half of `dev-key.pem` | Trust dev tokens in the tool handler, through `Verifier.WithDev` |
| `token.DevSigner()` | `DIVE_DEV_KEY` (PEM text or a path), else `dev-key.pem` | Mint tokens that `dive serve --dev-key` accepts |

Both refuse to load when `DIVE_ENV=production`. Run `dive serve --dev-key` once to create the key. An app in a container can take the public key through `DIVE_DEV_PUBLIC_KEY`.

## Start and watch runs

```go
client := dive.NewClient(os.Getenv("DIVE_URL"))

run, err := client.Start(ctx, start, "triage", TriageInput{Issue: "ENG-142"})

for entry, err := range client.Entries(ctx, read, run.ID, dive.WithDeltas(func(d dive.Delta) { fmt.Print(d.Text) })) {
	if err != nil {
		return err
	}
	log.Printf("%d %s %s", entry.Seq, entry.Kind, entry.Path)
}

view, err := client.Wait(ctx, read, run.ID)
result, err := dive.Result[TriageResult](view)
```

- `Entries` follows the server-sent event stream until `run_ended`. After a dropped connection it reconnects with `Last-Event-ID`, so no seq is missed or repeated. Options: `dive.After(seq)`, `dive.WithDeltas(fn)`, `dive.MaxReconnects(n)` (default 5) and `dive.ReconnectDelay(d)`.
- `Wait` returns the final `RunView`: status, outcome, result, error and usage.
- `Cancel`, `Run`, `Plans` and `Plan` cover the rest of the API.

### Errors

| Error | When |
| --- | --- |
| `*dive.APIError` | Any error response. `Code` is a typed constant such as `dive.CodeInvalidInput`; `Problems` lists input problems. |
| `*dive.AlreadyStartedError` | The start token already started a run. `Run` names it, so a retry after a dropped connection finds its run. |
| `dive.ErrIdempotencyViolation` | The token's `jti` already started a run that dive serve didn't name, such as one in another tenant. Use `errors.Is`. |
| `*dive.AtCapacityError` | The server or tenant is at its run limit. `RetryAfter` says when to retry. The SDK never retries a start itself. |

## Typed plan starts

```go
//go:generate go run github.com/loon-rocks/dive-go/cmd/dive-go-plans -dir .dive/plans -out plans/plans.go -package plans
```

For each plan this writes `<Plan>Input` from its `input_schema`, `<Plan>Result` from its `output_schema`, and methods on `plans.Plans`:

```go
p := plans.New(client)
run, err := p.StartFileIssue(ctx, start, plans.FileIssueInput{Title: "Login fails"})
result, view, err := p.WaitFileIssue(ctx, read, run.ID)
```

Optional properties become pointers, so an unset value is left out and the plan's default applies.

## Development

| Target | Does |
| --- | --- |
| `mise run build` | `go build ./...` |
| `mise run test` | `go test -race ./...` |
| `mise run lint` | gofmt, `go vet` and staticcheck |
| `mise run generate` | Regenerates `examples/issues` |
| `mise run generate:check` | Fails if `examples/issues` is stale |
| `mise run openapi:drift` | Diffs the wire types' copy of `openapi.json` against the pinned dive release |
| `mise run e2e` | Runs the client against a local `dive serve --dev-key` |

`mise run e2e` needs `dive` on `PATH`. It runs `dive serve` with a temporary `HOME`, so it never touches your config or data directory.
