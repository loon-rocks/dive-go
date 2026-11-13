package sample

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

// PlanTriage is the identifier of the plan "Triage".
//
// Triages an issue.
const PlanTriage = "triage"

// PlanWeeklyDigest is the identifier of the plan "Weekly digest".
//
// No inputs and no declared result.
const PlanWeeklyDigest = "weekly-digest"

// TriageInputReporter is the reporter property of [TriageInput].
type TriageInputReporter struct {
	APIKey *string `json:"api_key,omitempty"`
	Email  string  `json:"email"`
}

// TriageInput is the input of the triage plan.
type TriageInput struct {
	DryRun   *bool                `json:"dry_run,omitempty"`
	Issue    string               `json:"issue"`
	Labels   []string             `json:"labels,omitempty"`
	Limit    *int64               `json:"limit,omitempty"`
	Meta     map[string]float64   `json:"meta,omitempty"`
	Reporter *TriageInputReporter `json:"reporter,omitempty"`
}

// TriageResultRelatedItem is an item of the related property of
// [TriageResult].
type TriageResultRelatedItem struct {
	ID  *string `json:"id,omitempty"`
	URL *string `json:"url,omitempty"`
}

// TriageResult is the result of the triage plan.
type TriageResult struct {
	Priority string                    `json:"priority"`
	Raw      any                       `json:"raw,omitempty"`
	Related  []TriageResultRelatedItem `json:"related"`
	Score    *float64                  `json:"score,omitempty"`
	Team     string                    `json:"team"`
}

// WeeklyDigestInput is the input of the weekly-digest plan, which declares
// no input schema.
type WeeklyDigestInput struct{}

// StartTriage starts a run of the triage plan with in and returns once
// dive serve accepts it. tok is a start token for the plan. See
// [dive.Client.Start].
func (p *Plans) StartTriage(ctx context.Context, tok token.Token, in TriageInput, opts ...dive.StartOption) (*dive.Run, error) {
	return p.Client.Start(ctx, tok, PlanTriage, in, opts...)
}

// WaitTriage waits for a run of the triage plan to end and returns its
// result with the run's final state. tok needs the runs:read scope. When
// the run ends without a result, as when it fails, WaitTriage returns the
// final state and the error from [dive.Result]. See [dive.Client.Wait].
func (p *Plans) WaitTriage(ctx context.Context, tok token.Token, runID string) (TriageResult, *dive.RunView, error) {
	view, err := p.Client.Wait(ctx, tok, runID)
	if err != nil {
		return TriageResult{}, nil, err
	}
	result, err := dive.Result[TriageResult](view)
	return result, view, err
}

// StartWeeklyDigest starts a run of the weekly-digest plan with in and
// returns once dive serve accepts it. tok is a start token for the plan.
// See [dive.Client.Start].
func (p *Plans) StartWeeklyDigest(ctx context.Context, tok token.Token, in WeeklyDigestInput, opts ...dive.StartOption) (*dive.Run, error) {
	return p.Client.Start(ctx, tok, PlanWeeklyDigest, in, opts...)
}
