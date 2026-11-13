package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

// DefaultBaseURL is the base URL a [Registry] writes into tool files when
// [BaseURL] is omitted. Dive expands ${APP_URL} from its environment when
// it loads the files.
const DefaultBaseURL = "${APP_URL}/dive/tools"

// Generator is the value of the generated field in every tool file that
// [Registry.Generate] writes. Generate and [Registry.Check] treat files
// with any other value as written by hand.
const Generator = "dive-go"

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Def names and describes a tool in [Add].
type Def struct {
	// Name is the tool's name: lowercase letters, digits and _, starting
	// with a letter. Dive calls the tool user__<Name>.
	Name string

	// Description tells the model what the tool does. It can't be empty.
	Description string
}

// Registry holds the tools of an app. It serves them with
// [Registry.Handler] and writes their tool files with
// [Registry.Generate]. Create one with [New] and add tools with [Add].
//
// A Registry is safe for concurrent use by multiple goroutines.
type Registry struct {
	mu      sync.RWMutex
	baseURL string
	tools   map[string]*tool
}

// RegistryOption configures a [Registry] in [New].
type RegistryOption func(*Registry)

// BaseURL sets the base URL of the tools in the generated tool files.
// Each tool's URL is url, without trailing slashes, followed by "/" and
// the tool's name. When it is omitted, the base is [DefaultBaseURL].
func BaseURL(url string) RegistryOption {
	return func(r *Registry) { r.baseURL = strings.TrimRight(url, "/") }
}

// New returns an empty Registry.
func New(opts ...RegistryOption) *Registry {
	r := &Registry{baseURL: DefaultBaseURL, tools: map[string]*tool{}}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

type settings struct {
	readOnly bool
	retry    int
	retrySet bool
	timeout  time.Duration
	edit     func(in, out *jsonschema.Schema)
}

// Option configures one tool in [Add].
type Option func(*settings)

// ReadOnly marks the tool as having no side effects, with read_only: true
// in its tool file. When it is omitted, the tool is read_only: false.
func ReadOnly() Option {
	return func(s *settings) { s.readOnly = true }
}

// Retry sets how many attempts Dive makes at a call that fails with an
// error it may retry, such as one from [Unavailable]. It is written to
// the tool file as retry.attempts. attempts counts the first attempt, so
// Retry(3) retries a call twice. An attempts below 1 makes [Add] panic.
// When it is omitted, the tool file has no retry setting and Dive tries
// each call once.
func Retry(attempts int) Option {
	return func(s *settings) { s.retry, s.retrySet = attempts, true }
}

// Timeout limits how long one call of the tool may take. The handler
// cancels the context of a call after d, and a call still running then
// gets a 504 response with the code timeout, whatever the tool returns.
// The tool file gets timeout_secs, d in whole seconds rounded up, so Dive
// applies the same limit. d must be at least a second, or [Add] panics. When it is
// omitted, the handler sets no deadline and the tool file has no
// timeout_secs.
func Timeout(d time.Duration) Option {
	return func(s *settings) { s.timeout = d }
}

// Schema lets edit change the input and output schemas that [Add]
// reflects from the tool's types, for what struct tags can't express,
// such as an enum. The edited input schema is the one the handler
// validates calls against, and both go into the tool file.
func Schema(edit func(in, out *jsonschema.Schema)) Option {
	return func(s *settings) { s.edit = edit }
}

type tool struct {
	def      Def
	settings settings
	input    map[string]any
	output   map[string]any
	resolved *jsonschema.Resolved
	invoke   func(ctx context.Context, body []byte) (any, error)
}

// Add adds a tool to r that calls fn. The tool's input and output
// schemas are reflected from In and Out with
// [github.com/google/jsonschema-go/jsonschema]: json tags name the
// properties, a field without omitempty is required, and a jsonschema
// tag is the property's description. In must be a struct or a map.
//
// The handler decodes each call's input into an In and passes it to fn
// with a context that carries the [Call]; see [CallFrom]. fn returns the
// output, which is encoded as JSON, or an error. Return an error from
// [Errorf] or [Unavailable] for an error the run should see; any other
// error is logged and answered with a generic 500. See [Registry.Handler]
// for every response.
//
// Add panics when fn is nil, def has an invalid name or an empty
// description, an option is invalid, a schema can't be reflected, or r
// already has a tool with the name. These are programming errors, found
// the first time the code that builds the registry runs.
func Add[In, Out any](r *Registry, def Def, fn func(context.Context, In) (Out, error), opts ...Option) {
	if err := add(r, def, fn, opts); err != nil {
		panic(err)
	}
}

func add[In, Out any](r *Registry, def Def, fn func(context.Context, In) (Out, error), opts []Option) error {
	if fn == nil {
		return fmt.Errorf("tools: %s has no function", def.Name)
	}
	if !namePattern.MatchString(def.Name) {
		return fmt.Errorf("tools: %q isn't a valid tool name; use lowercase letters, digits and _, starting with a letter", def.Name)
	}
	if strings.TrimSpace(def.Description) == "" {
		return fmt.Errorf("tools: %s needs a description", def.Name)
	}
	var s settings
	for _, opt := range opts {
		opt(&s)
	}
	if s.retrySet && s.retry < 1 {
		return fmt.Errorf("tools: %s: Retry needs at least 1 attempt, got %d", def.Name, s.retry)
	}
	if s.timeout < 0 || (s.timeout > 0 && s.timeout < time.Second) {
		return fmt.Errorf("tools: %s: Timeout must be at least a second", def.Name)
	}
	in, err := jsonschema.For[In](nil)
	if err != nil {
		return fmt.Errorf("tools: %s: input schema: %w", def.Name, err)
	}
	out, err := jsonschema.For[Out](nil)
	if err != nil {
		return fmt.Errorf("tools: %s: output schema: %w", def.Name, err)
	}
	if in.Type != "object" {
		return fmt.Errorf("tools: %s: the input must be a struct or a map, so its schema is an object", def.Name)
	}
	if s.edit != nil {
		s.edit(in, out)
	}
	resolved, err := in.Resolve(nil)
	if err != nil {
		return fmt.Errorf("tools: %s: input schema: %w", def.Name, err)
	}
	inMap, err := schemaMap(in)
	if err != nil {
		return fmt.Errorf("tools: %s: input schema: %w", def.Name, err)
	}
	outMap, err := schemaMap(out)
	if err != nil {
		return fmt.Errorf("tools: %s: output schema: %w", def.Name, err)
	}
	t := &tool{
		def:      def,
		settings: s,
		input:    inMap,
		output:   outMap,
		resolved: resolved,
		invoke: func(ctx context.Context, body []byte) (any, error) {
			var input In
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&input); err != nil {
				return nil, &inputError{err}
			}
			return fn(ctx, input)
		},
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[def.Name]; ok {
		return fmt.Errorf("tools: %s is registered twice", def.Name)
	}
	r.tools[def.Name] = t
	return nil
}

func (r *Registry) lookup(name string) *tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

func (r *Registry) sorted() []*tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]*tool, 0, len(r.tools))
	for _, t := range r.tools {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].def.Name < list[j].def.Name })
	return list
}

// Names returns the names of the tools in r, sorted. It returns nil when
// r has no tools.
func (r *Registry) Names() []string {
	var names []string
	for _, t := range r.sorted() {
		names = append(names, t.def.Name)
	}
	return names
}

type inputError struct{ err error }

func (e *inputError) Error() string { return e.err.Error() }

func schemaMap(s *jsonschema.Schema) (map[string]any, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	normalized, ok := normalize(m).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema isn't an object")
	}
	return normalized, nil
}

func normalize(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, item := range v {
			v[k] = normalize(item)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = normalize(item)
		}
		return v
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		if f, err := v.Float64(); err == nil && !math.IsInf(f, 0) {
			return f
		}
		return v.String()
	}
	return v
}
