// Package tools defines Dive tools as Go functions, serves them over HTTP
// to the runs of dive serve, and generates the .dive/tools files that
// tell Dive how to call them.
//
// # Defining tools
//
// A tool is a function that takes a context and an input struct and
// returns an output struct. Add it to a [Registry] with [Add]:
//
//	type GetIssueInput struct {
//		ID string `json:"id" jsonschema:"The issue's id, such as ISS-1"`
//	}
//
//	type GetIssueOutput struct {
//		Title string `json:"title"`
//	}
//
//	r := tools.New()
//	tools.Add(r, tools.Def{
//		Name:        "get_issue",
//		Description: "Reads one issue by id.",
//	}, func(ctx context.Context, in GetIssueInput) (GetIssueOutput, error) {
//		call := tools.CallFrom(ctx)
//		issue, err := store.Get(call.Tenant, in.ID)
//		if err != nil {
//			return GetIssueOutput{}, tools.Errorf("no issue %s", in.ID)
//		}
//		return GetIssueOutput{Title: issue.Title}, nil
//	}, tools.ReadOnly())
//
// The input and output schemas are reflected from the types. [ReadOnly],
// [Retry], [Timeout] and [Schema] set the rest of the tool's definition.
// Inside the function, [CallFrom] returns the [Call]: the run, tenant,
// user and extra claims from the run token, and the call's idempotency
// key.
//
// # Serving tools
//
// [Registry.Handler] returns a [net/http.Handler] that serves every tool
// in the registry at its own path. Each call carries a run token that
// [HandlerOptions.Verifier], usually a [token.Verifier], must accept:
//
//	verifier := token.NewVerifier(appPublicKey, token.Issuer("https://app.example.com"))
//	mux.Handle("/dive/tools/", r.Handler(tools.HandlerOptions{Verifier: verifier}))
//
// # Errors
//
// A tool's error decides the response Dive gets:
//
//   - An error from [Errorf] refuses the call with 422 and the code
//     refused. Its message reaches the run, so the model can act on it.
//   - An error from [Unavailable] is a temporary failure, answered with
//     503 and the code unavailable. Dive retries the call as the tool's
//     [Retry] option allows. [RetryAfter] adds a Retry-After header.
//   - A tool that runs past its [Timeout] gets 504 and the code timeout,
//     whatever it returns.
//   - Any other error, or a panic, is answered with 500, the code internal
//     and a generic message. The response has a short ref that is logged
//     to [HandlerOptions.Logger] beside the real error, which never
//     reaches the response, so a tool can't leak internal details to the
//     model by accident.
//
// The handler answers on its own, before the tool runs, with 401 and the
// code unauthorized for a missing or invalid token or a closed run, 404
// or 405 and the code unknown_tool for an unknown tool or a method other
// than POST, and 400 and the code invalid_input for input that doesn't
// match the tool's schema. Every error body has the form
//
//	{"error": {"code": "refused", "message": "no issue ISS-9"}}
//
// [Registry.Handler] lists every response.
//
// # Generating tool files
//
// Dive learns about the tools from one YAML file per tool in the app's
// .dive/tools directory. [Registry.Generate] writes those files from the
// registry, marked generated: dive-go, and [Registry.Check] reports when
// they are out of date. Run Generate from a small program with
// go generate:
//
//	//go:generate go run ./cmd/divetools
//
//	func main() {
//		if err := app.DiveTools().Generate(".dive/tools"); err != nil {
//			log.Fatal(err)
//		}
//	}
//
// and call Check from a test so CI fails when someone forgets:
//
//	func TestDiveToolFilesMatchCode(t *testing.T) {
//		if err := app.DiveTools().Check(".dive/tools"); err != nil {
//			t.Fatal(err)
//		}
//	}
//
// Each file points Dive at the tool's URL, by default
// ${APP_URL}/dive/tools/<name>; change the base with [BaseURL]. Generate
// never overwrites a file written by hand. The module's examples/issues
// directory has a complete app.
package tools
