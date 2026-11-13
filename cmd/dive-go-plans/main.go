// Dive-go-plans generates a Go file with typed wrappers for the plans in
// a Dive project, for starting them with a [dive.Client].
//
// Usage:
//
//	dive-go-plans [flags]
//
// The flags are:
//
//	-dir directory
//		The directory of plan files to read. The default is .dive/plans.
//	-out file
//		The Go file to write. The default is dive_plans.go.
//	-package name
//		The package name of the Go file. The default is the name of the
//		directory the file is written to.
//	-plans identifiers
//		A comma-separated list of the plan identifiers to include. The
//		default is every plan.
//
// For each .yaml or .yml plan file, the generated file has a Plan
// constant with the plan's identifier, an Input struct from the plan's
// input_schema, a Result struct from its output_schema when it has one,
// and Start and Wait methods on a Plans type that wraps a [dive.Client].
// A plan "triage" becomes PlanTriage, TriageInput, TriageResult,
// Plans.StartTriage and Plans.WaitTriage. Required properties become
// plain fields; optional and nullable ones become pointers unless the Go
// type can already be nil. Nested objects get their own struct types.
//
// The output is gofmt-formatted and the same on every run, and the file
// is left untouched when it wouldn't change. Run it with go generate:
//
//	//go:generate go run github.com/loon-rocks/dive-go/cmd/dive-go-plans -out plans/plans.go
//
// [dive.Client]: https://pkg.go.dev/github.com/loon-rocks/dive-go#Client
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

func main() {
	dir := flag.String("dir", ".dive/plans", "directory of plan files")
	out := flag.String("out", "dive_plans.go", "Go file to write")
	pkg := flag.String("package", "", "package name; defaults to the output directory's name")
	only := flag.String("plans", "", "comma-separated plan identifiers to include; all when empty")
	flag.Parse()
	name := *pkg
	if name == "" {
		abs, err := filepath.Abs(*out)
		if err != nil {
			fail(err)
		}
		name = filepath.Base(filepath.Dir(abs))
	}
	var include []string
	if *only != "" {
		include = strings.Split(*only, ",")
	}
	code, err := Generate(*dir, name, include)
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fail(err)
	}
	if existing, err := os.ReadFile(*out); err == nil && bytes.Equal(existing, code) {
		return
	}
	if err := os.WriteFile(*out, code, 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "dive-go-plans:", err)
	os.Exit(1)
}

type planFile struct {
	Identifier   string         `yaml:"identifier"`
	Name         string         `yaml:"name"`
	Description  string         `yaml:"description"`
	InputSchema  map[string]any `yaml:"input_schema"`
	OutputSchema map[string]any `yaml:"output_schema"`
}

type plan struct {
	id          string
	goName      string
	name        string
	description string
	input       map[string]any
	output      map[string]any
}

func load(dir string, include []string) ([]plan, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, id := range include {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	var plans []plan
	seen := map[string]string{}
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if entry.IsDir() || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var f planFile
		if err := yaml.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		id := f.Identifier
		if id == "" {
			id = strings.TrimSuffix(entry.Name(), ext)
		}
		if len(wanted) > 0 && !wanted[id] {
			continue
		}
		goName := pascal(id)
		if other, ok := seen[goName]; ok {
			return nil, fmt.Errorf("plans %s and %s both become %s in Go", other, id, goName)
		}
		seen[goName] = id
		plans = append(plans, plan{id: id, goName: goName, name: f.Name, description: f.Description, input: f.InputSchema, output: f.OutputSchema})
	}
	if len(plans) == 0 {
		return nil, errors.New("no plans found in " + dir)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].id < plans[j].id })
	return plans, nil
}

// Generate returns the Go source of package pkg for the plan files in
// dir, limited to the plans whose identifiers are in include when include
// isn't empty. The code is gofmt-formatted and the same on every run.
func Generate(dir, pkg string, include []string) ([]byte, error) {
	plans, err := load(dir, include)
	if err != nil {
		return nil, err
	}
	g := &gen{}
	for _, p := range plans {
		if p.input == nil {
			g.types = append(g.types, docComment("", fmt.Sprintf("%sInput is the input of the %s plan, which declares no input schema.", p.goName, p.id))+fmt.Sprintf("type %sInput struct{}\n", p.goName))
		} else if _, err := g.structType(p.goName+"Input", "the input of the "+p.id+" plan", p.input); err != nil {
			return nil, fmt.Errorf("plan %s input_schema: %w", p.id, err)
		}
		if p.output != nil {
			if _, err := g.structType(p.goName+"Result", "the result of the "+p.id+" plan", p.output); err != nil {
				return nil, fmt.Errorf("plan %s output_schema: %w", p.id, err)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("import (\n\t\"context\"\n")
	b.WriteString("\n\tdive \"github.com/loon-rocks/dive-go\"\n\t\"github.com/loon-rocks/dive-go/token\"\n)\n\n")
	b.WriteString(docComment("", "Plans starts runs of the plans in this package with typed input, and waits for their typed results, through a [dive.Client]."))
	b.WriteString("type Plans struct {\n")
	b.WriteString(docComment("\t", "Client is the client that starts and waits for the runs."))
	b.WriteString("\tClient *dive.Client\n}\n\n")
	b.WriteString(docComment("", "New returns a Plans that uses client."))
	b.WriteString("func New(client *dive.Client) *Plans {\n\treturn &Plans{Client: client}\n}\n\n")
	for _, p := range plans {
		title := p.name
		if strings.TrimSpace(title) == "" {
			title = p.id
		}
		b.WriteString(docComment("", fmt.Sprintf("Plan%s is the identifier of the plan %s.", p.goName, strconv.Quote(strings.Join(strings.Fields(title), " "))), p.description))
		fmt.Fprintf(&b, "const Plan%s = %s\n\n", p.goName, strconv.Quote(p.id))
	}
	for _, t := range g.types {
		b.WriteString(t)
		b.WriteString("\n")
	}
	for _, p := range plans {
		b.WriteString(docComment("", fmt.Sprintf("Start%s starts a run of the %s plan with in and returns once dive serve accepts it. tok is a start token for the plan. See [dive.Client.Start].", p.goName, p.id)))
		fmt.Fprintf(&b, "func (p *Plans) Start%s(ctx context.Context, tok token.Token, in %sInput, opts ...dive.StartOption) (*dive.Run, error) {\n", p.goName, p.goName)
		fmt.Fprintf(&b, "\treturn p.Client.Start(ctx, tok, Plan%s, in, opts...)\n}\n\n", p.goName)
		if p.output != nil {
			b.WriteString(docComment("", fmt.Sprintf("Wait%s waits for a run of the %s plan to end and returns its result with the run's final state. tok needs the runs:read scope. When the run ends without a result, as when it fails, Wait%s returns the final state and the error from [dive.Result]. See [dive.Client.Wait].", p.goName, p.id, p.goName)))
			fmt.Fprintf(&b, "func (p *Plans) Wait%s(ctx context.Context, tok token.Token, runID string) (%sResult, *dive.RunView, error) {\n", p.goName, p.goName)
			b.WriteString("\tview, err := p.Client.Wait(ctx, tok, runID)\n\tif err != nil {\n")
			fmt.Fprintf(&b, "\t\treturn %sResult{}, nil, err\n\t}\n", p.goName)
			fmt.Fprintf(&b, "\tresult, err := dive.Result[%sResult](view)\n\treturn result, view, err\n}\n\n", p.goName)
		}
	}
	code, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("formatting the generated code: %w\n%s", err, b.String())
	}
	return code, nil
}

type gen struct {
	types []string
}

func (g *gen) structType(name, doc string, schema map[string]any) (string, error) {
	props, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	if list, ok := schema["required"].([]any); ok {
		for _, r := range list {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	keys := slices.Sorted(maps.Keys(props))
	var b strings.Builder
	b.WriteString(docComment("", name+" is "+doc+"."))
	fmt.Fprintf(&b, "type %s struct {\n", name)
	used := map[string]bool{}
	for _, key := range keys {
		prop, _ := props[key].(map[string]any)
		field := pascal(key)
		if field == "" || used[field] {
			return "", fmt.Errorf("property %q has no unique Go name", key)
		}
		used[field] = true
		goType, nullable, err := g.typeOf(name+field, fmt.Sprintf("the %s property of [%s]", key, name), prop)
		if err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
		tag := key
		if !required[key] {
			tag += ",omitempty"
		}
		if (!required[key] || nullable) && !canBeNil(goType) {
			goType = "*" + goType
		}
		fmt.Fprintf(&b, "\t%s %s `json:%s`\n", field, goType, strconv.Quote(tag))
	}
	b.WriteString("}\n")
	g.types = append(g.types, b.String())
	return name, nil
}

func docComment(indent string, paragraphs ...string) string {
	var b strings.Builder
	first := true
	for _, paragraph := range paragraphs {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			continue
		}
		if !first {
			b.WriteString(indent + "//\n")
		}
		first = false
		line := ""
		for _, word := range words {
			if line != "" && len(line)+1+len(word) > 72 {
				b.WriteString(indent + "// " + line + "\n")
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		b.WriteString(indent + "// " + line + "\n")
	}
	return b.String()
}

func canBeNil(goType string) bool {
	return strings.HasPrefix(goType, "[]") || strings.HasPrefix(goType, "map[") || goType == "any"
}

func (g *gen) typeOf(name, doc string, schema map[string]any) (string, bool, error) {
	if schema == nil {
		return "any", false, nil
	}
	types, nullable := schemaTypes(schema)
	if len(types) != 1 {
		return "any", nullable, nil
	}
	switch types[0] {
	case "string":
		return "string", nullable, nil
	case "integer":
		return "int64", nullable, nil
	case "number":
		return "float64", nullable, nil
	case "boolean":
		return "bool", nullable, nil
	case "array":
		items, _ := schema["items"].(map[string]any)
		inner, _, err := g.typeOf(name+"Item", "an item of "+doc, items)
		if err != nil {
			return "", false, err
		}
		return "[]" + inner, nullable, nil
	case "object":
		if props, ok := schema["properties"].(map[string]any); ok && len(props) > 0 {
			t, err := g.structType(name, doc, schema)
			return t, nullable, err
		}
		if extra, ok := schema["additionalProperties"].(map[string]any); ok {
			inner, _, err := g.typeOf(name+"Value", "a value of "+doc, extra)
			if err != nil {
				return "", false, err
			}
			return "map[string]" + inner, nullable, nil
		}
		return "map[string]any", nullable, nil
	}
	return "any", nullable, nil
}

func schemaTypes(schema map[string]any) ([]string, bool) {
	var types []string
	nullable := false
	switch t := schema["type"].(type) {
	case string:
		types = []string{t}
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok {
				types = append(types, s)
			}
		}
	}
	var out []string
	for _, t := range types {
		if t == "null" {
			nullable = true
			continue
		}
		out = append(out, t)
	}
	return out, nullable
}

var initialisms = map[string]string{
	"id": "ID", "url": "URL", "uri": "URI", "http": "HTTP", "https": "HTTPS", "api": "API", "json": "JSON",
	"uuid": "UUID", "ip": "IP", "sql": "SQL", "html": "HTML", "ui": "UI", "ttl": "TTL", "pr": "PR",
}

func pascal(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var b strings.Builder
	for _, part := range parts {
		if up, ok := initialisms[strings.ToLower(part)]; ok {
			b.WriteString(up)
			continue
		}
		runes := []rune(part)
		runes[0] = unicode.ToUpper(runes[0])
		b.WriteString(string(runes))
	}
	out := b.String()
	if out != "" && unicode.IsDigit([]rune(out)[0]) {
		out = "P" + out
	}
	return out
}
