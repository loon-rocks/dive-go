package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite internal/sample/plans.go")

func TestGenerateMatchesTheCompiledSample(t *testing.T) {
	code, err := Generate("testdata/plans", "sample", nil)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("internal", "sample", "plans.go")
	if *update {
		if err := os.WriteFile(golden, code, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(code) {
		t.Fatalf("generated code differs from %s; run go test ./cmd/dive-go-plans -update\n%s", golden, code)
	}
	for _, line := range []string{
		"func (p *Plans) StartTriage(ctx context.Context, tok token.Token, in TriageInput, opts ...dive.StartOption) (*dive.Run, error)",
		"func (p *Plans) WaitTriage(ctx context.Context, tok token.Token, runID string) (TriageResult, *dive.RunView, error)",
		"func (p *Plans) StartWeeklyDigest(ctx context.Context, tok token.Token, in WeeklyDigestInput",
		"Issue    string                 `json:\"issue\"`",
		"Limit    *int64",
		"APIKey *string `json:\"api_key,omitempty\"`",
		"Related  []TriageResultRelatedItem `json:\"related\"`",
		"Meta     map[string]float64",
		"Raw      any",
		"const PlanWeeklyDigest = \"weekly-digest\"",
		"// PlanTriage is the identifier of the plan \"Triage\". // // Triages an issue.",
		"// TriageResultRelatedItem is an item of the related property of // [TriageResult].",
		"// WaitTriage waits for a run of the triage plan to end",
	} {
		if !strings.Contains(normalizeSpaces(string(code)), normalizeSpaces(line)) {
			t.Errorf("missing %q", line)
		}
	}
	if strings.Contains(string(code), "WaitWeeklyDigest") {
		t.Error("unexpected output")
	}
	lines := strings.Split(string(code), "\n")
	for i, line := range lines {
		for _, prefix := range []string{"type ", "func ", "const ", "\tClient "} {
			if strings.HasPrefix(line, prefix) && (i == 0 || !strings.HasPrefix(strings.TrimSpace(lines[i-1]), "// ")) {
				t.Errorf("no doc comment above %q", line)
			}
		}
	}
	again, _ := Generate("testdata/plans", "sample", nil)
	if string(again) != string(code) {
		t.Fatal("output isn't stable")
	}
}

func TestGenerateFiltersAndFails(t *testing.T) {
	code, err := Generate("testdata/plans", "sample", []string{"weekly-digest"})
	if err != nil || strings.Contains(string(code), "Triage") {
		t.Fatalf("filter: %v", err)
	}
	if _, err := Generate(t.TempDir(), "x", nil); err == nil {
		t.Fatal("an empty directory must fail")
	}
}

func TestPascal(t *testing.T) {
	cases := map[string]string{"file_issue": "FileIssue", "weekly-digest": "WeeklyDigest", "id": "ID", "html_url": "HTMLURL", "2fa": "P2fa"}
	for in, want := range cases {
		if got := pascal(in); got != want {
			t.Errorf("pascal(%q) = %q, want %q", in, got, want)
		}
	}
}

func normalizeSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
