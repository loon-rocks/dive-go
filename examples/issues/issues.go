//go:generate go run ./cmd/divetools
//go:generate go run ../../cmd/dive-go-plans -dir .dive/plans -out plans/plans.go -package plans

package issues

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/loon-rocks/dive-go/tools"
)

// ErrNotFound is returned by [Store.Get] when the tenant has no issue
// with the id.
var ErrNotFound = errors.New("no such issue")

// Issue is an issue in a tenant's tracker.
type Issue struct {
	// ID is the issue's id, such as ISS-1.
	ID string

	// Tenant is the tenant the issue belongs to.
	Tenant string

	// Title is the one-line summary.
	Title string

	// Body is the Markdown details.
	Body string

	// Priority is low, normal or urgent.
	Priority string

	// CreatedBy is the user who filed the issue.
	CreatedBy string
}

// Store keeps issues in memory, separately for each tenant. It is safe
// for concurrent use by multiple goroutines.
type Store struct {
	mu       sync.Mutex
	byTenant map[string]map[string]Issue
	byKey    map[string]string
	next     int
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{byTenant: map[string]map[string]Issue{}, byKey: map[string]string{}}
}

// Create files an issue in tenant's tracker and returns it. When
// idempotencyKey isn't empty and an earlier Create in the same tenant used
// it, Create returns that issue instead of filing another.
func (s *Store) Create(tenant, user, title, body, priority, idempotencyKey string) Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idempotencyKey != "" {
		if id, ok := s.byKey[tenant+"/"+idempotencyKey]; ok {
			return s.byTenant[tenant][id]
		}
	}
	s.next++
	issue := Issue{ID: fmt.Sprintf("ISS-%d", s.next), Tenant: tenant, Title: title, Body: body, Priority: priority, CreatedBy: user}
	if s.byTenant[tenant] == nil {
		s.byTenant[tenant] = map[string]Issue{}
	}
	s.byTenant[tenant][issue.ID] = issue
	if idempotencyKey != "" {
		s.byKey[tenant+"/"+idempotencyKey] = issue.ID
	}
	return issue
}

// Get returns the issue with the given id in tenant's tracker, or
// [ErrNotFound].
func (s *Store) Get(tenant, id string) (Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	issue, ok := s.byTenant[tenant][id]
	if !ok {
		return Issue{}, ErrNotFound
	}
	return issue, nil
}

// CreateIssueInput is the input of the create_issue tool.
type CreateIssueInput struct {
	Title    string `json:"title" jsonschema:"One-line summary of the problem"`
	Body     string `json:"body,omitempty" jsonschema:"Markdown details, steps to reproduce"`
	Priority string `json:"priority,omitempty" jsonschema:"low, normal or urgent; normal when unsure"`
}

// CreateIssueOutput is the output of the create_issue tool.
type CreateIssueOutput struct {
	ID  string `json:"id" jsonschema:"The new issue's id"`
	URL string `json:"url" jsonschema:"Where people read the issue"`
}

// GetIssueInput is the input of the get_issue tool.
type GetIssueInput struct {
	ID string `json:"id" jsonschema:"The issue's id, such as ISS-1"`
}

// GetIssueOutput is the output of the get_issue tool.
type GetIssueOutput struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	Priority  string `json:"priority"`
	Tenant    string `json:"tenant" jsonschema:"The workspace the issue belongs to"`
	CreatedBy string `json:"created_by" jsonschema:"Who filed the issue"`
}

// DiveTools returns the app's Dive tools, create_issue and get_issue,
// backed by store. Each call is logged to log, or to [slog.Default] when
// log is nil. create_issue refuses users whose role claim is viewer.
func DiveTools(store *Store, log *slog.Logger) *tools.Registry {
	if log == nil {
		log = slog.Default()
	}
	r := tools.New()
	tools.Add(r, tools.Def{
		Name:        "create_issue",
		Description: "Creates an issue in the tenant's tracker and returns its id and URL.",
	}, func(ctx context.Context, in CreateIssueInput) (CreateIssueOutput, error) {
		call := tools.CallFrom(ctx)
		log.InfoContext(ctx, "create_issue", "tenant", call.Tenant, "user", call.User, "run_id", call.RunID, "dev", call.Dev)
		if role, _ := call.Claims["role"].(string); role == "viewer" {
			return CreateIssueOutput{}, tools.Errorf("%s is a viewer in %s and can't create issues", call.User, call.Tenant)
		}
		priority := in.Priority
		if priority == "" {
			priority = "normal"
		}
		issue := store.Create(call.Tenant, call.User, in.Title, in.Body, priority, call.IdempotencyKey)
		return CreateIssueOutput{ID: issue.ID, URL: "https://issues.example.com/" + call.Tenant + "/" + issue.ID}, nil
	}, tools.Retry(3), tools.Schema(func(in, _ *jsonschema.Schema) {
		in.Properties["priority"].Enum = []any{"low", "normal", "urgent"}
	}))
	tools.Add(r, tools.Def{
		Name:        "get_issue",
		Description: "Reads one issue by id, with the tenant it belongs to and who filed it.",
	}, func(ctx context.Context, in GetIssueInput) (GetIssueOutput, error) {
		call := tools.CallFrom(ctx)
		log.InfoContext(ctx, "get_issue", "tenant", call.Tenant, "user", call.User, "run_id", call.RunID, "dev", call.Dev)
		issue, err := store.Get(call.Tenant, in.ID)
		if errors.Is(err, ErrNotFound) {
			return GetIssueOutput{}, tools.Errorf("no issue %s in %s", in.ID, call.Tenant)
		}
		if err != nil {
			return GetIssueOutput{}, err
		}
		return GetIssueOutput{ID: issue.ID, Title: issue.Title, Body: issue.Body, Priority: issue.Priority, Tenant: issue.Tenant, CreatedBy: issue.CreatedBy}, nil
	}, tools.ReadOnly())
	return r
}
