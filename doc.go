// Package dive is a client for the HTTP API of dive serve, the process
// that runs Dive plans as a service. It starts plan runs, follows their
// entries as they are written, waits for them to end, and cancels them.
//
// The packages beside it cover the rest of an app that uses Dive:
// [github.com/loon-rocks/dive-go/token] mints the run tokens that every
// request carries, and [github.com/loon-rocks/dive-go/tools] serves the
// app's own tools to the runs.
//
// # Starting a run
//
// Create a [Client] with [NewClient] and the address of dive serve. Mint a
// start token for the plan with [token.Signer.Start] and pass it to
// [Client.Start] with the plan's input:
//
//	client := dive.NewClient("http://127.0.0.1:8081")
//	start, _, err := signer.Start(token.StartClaims{Plan: "triage", Tenant: "acme", User: "ada@example.com"})
//	if err != nil {
//		return err
//	}
//	run, err := client.Start(ctx, start, "triage", TriageInput{Issue: "ENG-142"})
//
// Start returns once dive serve accepts the run. The start token's jti is
// the run's id, and a token starts at most one run: starting again with
// the same token returns an [*AlreadyStartedError] naming the first run,
// so a start whose response was lost can be retried with the same token.
//
// # Following a run
//
// The other methods take a read token from [token.Signer.Reader].
// [Client.Entries] returns an iterator over the run's entries that follows
// the run live and ends after the entry of kind [KindRunEnded]. It
// reconnects after a dropped connection without missing or repeating an
// entry:
//
//	for entry, err := range client.Entries(ctx, read, run.ID) {
//		if err != nil {
//			return err
//		}
//		fmt.Println(entry.Seq, entry.Kind, entry.Path)
//	}
//
// [Client.Wait] blocks until the run ends and returns its final
// [RunView]; [Result] decodes the plan's output. [Client.Run] reads a run
// without waiting, and [Client.Cancel] asks dive serve to stop it.
//
// # Errors
//
// An error response from dive serve is an [*APIError] whose [Code] says
// what went wrong. [*AlreadyStartedError] and [*AtCapacityError] wrap an
// APIError for the two cases a caller usually handles on its own.
//
// # Typed plans
//
// The dive-go-plans command in cmd/dive-go-plans generates a Go file with
// an input and result type and Start and Wait methods for each plan in a
// .dive/plans directory.
package dive
