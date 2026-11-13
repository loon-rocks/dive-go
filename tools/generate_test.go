package tools_test

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/loon-rocks/dive-go/tools"
)

var update = flag.Bool("update", false, "rewrite the golden files")

type FlatInput struct {
	Title    string  `json:"title" jsonschema:"One-line summary of the problem"`
	Body     string  `json:"body,omitempty" jsonschema:"Markdown details"`
	Count    int     `json:"count"`
	Ratio    float64 `json:"ratio,omitempty"`
	Urgent   bool    `json:"urgent,omitempty"`
	Internal string  `json:"-"`
}

type FlatOutput struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

type Label struct {
	Name  string `json:"name" jsonschema:"The label's name"`
	Color string `json:"color,omitempty"`
}

type NestedInput struct {
	Issue struct {
		Title  string  `json:"title"`
		Labels []Label `json:"labels,omitempty"`
	} `json:"issue"`
	Assignee *Label            `json:"assignee,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
	Due      time.Time         `json:"due,omitempty"`
	Tags     []string          `json:"tags"`
	Bytes    [2]int            `json:"bytes,omitempty"`
}

type NestedOutput struct {
	Labels []Label        `json:"labels"`
	Extra  map[string]any `json:"extra,omitempty"`
}

type EmptyInput struct{}

func noop[In, Out any](_ context.Context, _ In) (Out, error) {
	var out Out
	return out, nil
}

func shapes() *tools.Registry {
	r := tools.New()
	tools.Add(r, tools.Def{Name: "flat", Description: "Flat input with optional fields."}, noop[FlatInput, FlatOutput])
	tools.Add(r, tools.Def{Name: "nested", Description: "Nested structs, slices, maps, pointers and time."}, noop[NestedInput, NestedOutput], tools.ReadOnly())
	tools.Add(r, tools.Def{Name: "options", Description: "Retries, a timeout and an enum from the Schema hook."}, noop[FlatInput, FlatOutput],
		tools.Retry(3),
		tools.Timeout(1500*time.Millisecond),
		tools.Schema(func(in, out *jsonschema.Schema) {
			in.Properties["body"].Enum = []any{"low", "normal", "urgent"}
			in.Properties["count"].Minimum = jsonschema.Ptr(1.0)
			out.Properties["url"].Format = "uri"
		}))
	tools.Add(r, tools.Def{Name: "empty", Description: "No input and a map output."}, noop[EmptyInput, map[string]int], tools.ReadOnly())
	return r
}

func TestSchemaGoldenFiles(t *testing.T) {
	files, err := shapes().Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("got %d files", len(files))
	}
	for file, data := range files {
		golden := filepath.Join("testdata", "golden", file)
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, data, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%s: %v (run go test ./tools -update)", file, err)
		}
		if string(want) != string(data) {
			t.Errorf("%s differs from its golden file:\n%s", file, data)
		}
	}
}

func TestGeneratedFileHasTheContractFields(t *testing.T) {
	r := tools.New(tools.BaseURL("http://localhost:8080/dive/tools/"))
	tools.Add(r, tools.Def{Name: "create_issue", Description: "Creates an issue."}, noop[FlatInput, FlatOutput], tools.Retry(2))
	files, err := r.Files()
	if err != nil {
		t.Fatal(err)
	}
	got := string(files["create_issue.yaml"])
	for _, want := range []string{
		"version: 5\n", "name: create_issue\n", "kind: http\n", "generated: dive-go\n", "method: POST\n",
		"url: http://localhost:8080/dive/tools/create_issue\n", "read_only: false\n", "retry:\n  attempts: 2\n",
		"input_schema:\n", "output_schema:\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "timeout_secs") || strings.Contains(got, "#") {
		t.Errorf("unexpected content:\n%s", got)
	}
}

func TestGenerateIsByteStable(t *testing.T) {
	dir := t.TempDir()
	if err := shapes().Generate(dir); err != nil {
		t.Fatal(err)
	}
	first := readDir(t, dir)
	stat, _ := os.Stat(filepath.Join(dir, "flat.yaml"))
	time.Sleep(10 * time.Millisecond)
	if err := shapes().Generate(dir); err != nil {
		t.Fatal(err)
	}
	second := readDir(t, dir)
	if len(first) != len(second) {
		t.Fatal("file count changed")
	}
	for file, data := range first {
		if second[file] != data {
			t.Errorf("%s changed between runs", file)
		}
	}
	again, _ := os.Stat(filepath.Join(dir, "flat.yaml"))
	if !again.ModTime().Equal(stat.ModTime()) {
		t.Error("an unchanged file was rewritten")
	}
	if err := shapes().Check(dir); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateRemovesStaleFilesAndLeavesHandWrittenOnes(t *testing.T) {
	dir := t.TempDir()
	if err := shapes().Generate(dir); err != nil {
		t.Fatal(err)
	}
	hand := "version: 4\nname: deploy\ndescription: Hand-written.\nkind: exec\ncommand: ./deploy.sh\n"
	writeFile(t, dir, "deploy.yaml", hand)
	writeFile(t, dir, "notes.txt", "not a tool")
	r := tools.New()
	tools.Add(r, tools.Def{Name: "flat", Description: "Flat input with optional fields."}, noop[FlatInput, FlatOutput])
	if err := r.Check(dir); err == nil {
		t.Fatal("Check must report the stale files")
	}
	if err := r.Generate(dir); err != nil {
		t.Fatal(err)
	}
	got := readDir(t, dir)
	for _, gone := range []string{"nested.yaml", "options.yaml", "empty.yaml"} {
		if _, ok := got[gone]; ok {
			t.Errorf("%s should be removed", gone)
		}
	}
	if got["deploy.yaml"] != hand || got["notes.txt"] != "not a tool" {
		t.Error("files without generated: dive-go must be untouched")
	}
	if _, ok := got["flat.yaml"]; !ok {
		t.Error("flat.yaml is missing")
	}
	if err := r.Check(dir); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateRefusesToOverwriteAHandWrittenFile(t *testing.T) {
	dir := t.TempDir()
	hand := "version: 4\nname: flat\ndescription: Mine.\nkind: exec\ncommand: echo\n"
	writeFile(t, dir, "flat.yaml", hand)
	err := shapes().Generate(dir)
	if err == nil || !strings.Contains(err.Error(), "flat.yaml") {
		t.Fatalf("got %v", err)
	}
	got := readDir(t, dir)
	if got["flat.yaml"] != hand || len(got) != 1 {
		t.Fatal("a refused Generate must write nothing")
	}
	other := t.TempDir()
	writeFile(t, other, "my_flat.yml", hand)
	if err := shapes().Generate(other); err == nil {
		t.Fatal("a hand-written file naming the same tool must be refused")
	}
	var check *tools.CheckError
	if err := shapes().Check(dir); !errors.As(err, &check) || !strings.Contains(err.Error(), "written by hand") {
		t.Fatalf("got %v", err)
	}
}

type v1Input struct {
	Title string `json:"title"`
}

type v2Input struct {
	Title    string `json:"title"`
	Priority string `json:"priority" jsonschema:"low, normal or urgent"`
}

func TestCheckReportsAPerFileDiff(t *testing.T) {
	dir := t.TempDir()
	before := tools.New()
	tools.Add(before, tools.Def{Name: "create_issue", Description: "Creates an issue."}, noop[v1Input, FlatOutput])
	if err := before.Generate(dir); err != nil {
		t.Fatal(err)
	}
	after := tools.New()
	tools.Add(after, tools.Def{Name: "create_issue", Description: "Creates an issue."}, noop[v2Input, FlatOutput])
	tools.Add(after, tools.Def{Name: "get_issue", Description: "Reads one issue."}, noop[v1Input, FlatOutput])
	err := after.Check(dir)
	var check *tools.CheckError
	if !errors.As(err, &check) {
		t.Fatalf("got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"run go generate",
		"create_issue.yaml: differs from the code",
		"--- create_issue.yaml (on disk)",
		"+++ create_issue.yaml (from code)",
		"+    priority:",
		"+      description: low, normal or urgent",
		"+    - priority",
		"get_issue.yaml: missing",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in\n%s", want, msg)
		}
	}
	if len(check.Problems) != 2 {
		t.Fatalf("problems %+v", check.Problems)
	}
	if err := after.Generate(dir); err != nil {
		t.Fatal(err)
	}
	if err := after.Check(dir); err != nil {
		t.Fatal(err)
	}
}

func TestAddRefusesBadDefinitions(t *testing.T) {
	cases := map[string]func(r *tools.Registry){
		"bad name": func(r *tools.Registry) {
			tools.Add(r, tools.Def{Name: "Create-Issue", Description: "x"}, noop[v1Input, FlatOutput])
		},
		"no desc": func(r *tools.Registry) { tools.Add(r, tools.Def{Name: "x"}, noop[v1Input, FlatOutput]) },
		"twice": func(r *tools.Registry) {
			tools.Add(r, tools.Def{Name: "flat", Description: "x"}, noop[v1Input, FlatOutput])
		},
		"not object": func(r *tools.Registry) {
			tools.Add(r, tools.Def{Name: "s", Description: "x"}, noop[string, FlatOutput])
		},
		"no attempts": func(r *tools.Registry) {
			tools.Add(r, tools.Def{Name: "r0", Description: "x"}, noop[v1Input, FlatOutput], tools.Retry(0))
		},
		"negative attempts": func(r *tools.Registry) {
			tools.Add(r, tools.Def{Name: "rn", Description: "x"}, noop[v1Input, FlatOutput], tools.Retry(-1))
		},
		"short timeout": func(r *tools.Registry) {
			tools.Add(r, tools.Def{Name: "t", Description: "x"}, noop[v1Input, FlatOutput], tools.Timeout(time.Millisecond))
		},
	}
	for name, add := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			add(shapes())
		})
	}
}

func readDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(data)
	}
	return out
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
