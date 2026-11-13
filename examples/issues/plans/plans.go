package plans

import (
	"context"

	dive "github.com/loon-rocks/dive-go"
	"github.com/loon-rocks/dive-go/token"
)

// Plans starts runs of the plans in this package with typed input, and
// waits for their typed results, through a [dive.Client].
type Plans struct {
	// Client is the client that starts and waits for the runs.
	Client *dive.Client
}

// New returns a Plans that uses client.
func New(client *dive.Client) *Plans {
	return &Plans{Client: client}
}

// PlanFileIssue is the identifier of the plan "File an issue".
//
// Files an issue in the caller's tracker, reads it back and reports which
// tenant and user it was filed for.
const PlanFileIssue = "file_issue"

// FileIssueInput is the input of the file_issue plan.
type FileIssueInput struct {
	Body     *string `json:"body,omitempty"`
	Priority *string `json:"priority,omitempty"`
	Title    string  `json:"title"`
}

// FileIssueResult is the result of the file_issue plan.
type FileIssueResult struct {
	CreatedBy string `json:"created_by"`
	ID        string `json:"id"`
	Tenant    string `json:"tenant"`
	URL       string `json:"url"`
}

// StartFileIssue starts a run of the file_issue plan with in and returns
// once dive serve accepts it. tok is a start token for the plan. See
// [dive.Client.Start].
func (p *Plans) StartFileIssue(ctx context.Context, tok token.Token, in FileIssueInput, opts ...dive.StartOption) (*dive.Run, error) {
	return p.Client.Start(ctx, tok, PlanFileIssue, in, opts...)
}

// WaitFileIssue waits for a run of the file_issue plan to end and returns
// its result with the run's final state. tok needs the runs:read scope.
// When the run ends without a result, as when it fails, WaitFileIssue
// returns the final state and the error from [dive.Result]. See
// [dive.Client.Wait].
func (p *Plans) WaitFileIssue(ctx context.Context, tok token.Token, runID string) (FileIssueResult, *dive.RunView, error) {
	view, err := p.Client.Wait(ctx, tok, runID)
	if err != nil {
		return FileIssueResult{}, nil, err
	}
	result, err := dive.Result[FileIssueResult](view)
	return result, view, err
}
