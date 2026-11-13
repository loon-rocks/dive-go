package wire

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type schema struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

func TestWireTypesMatchOpenAPI(t *testing.T) {
	data, err := os.ReadFile("testdata/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	types := map[string]any{
		"EntryDoc":    EntryDoc{},
		"ErrorBody":   ErrorBody{},
		"ErrorDetail": ErrorDetail{},
		"PlanBody":    PlanBody{},
		"PlanList":    PlanList{},
		"RunBody":     RunBody{},
		"StartBody":   StartBody{},
		"StartedBody": StartedBody{},
	}
	var names []string
	for name := range doc.Components.Schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value, ok := types[name]
		if !ok {
			t.Errorf("openapi.json has schema %s with no wire type", name)
			continue
		}
		s := doc.Components.Schemas[name]
		fields := map[string]bool{}
		rt := reflect.TypeOf(value)
		for i := 0; i < rt.NumField(); i++ {
			tag := rt.Field(i).Tag.Get("json")
			jsonName, opts, _ := strings.Cut(tag, ",")
			fields[jsonName] = true
			if _, ok := s.Properties[jsonName]; !ok {
				t.Errorf("%s.%s isn't in openapi.json", name, jsonName)
			}
			required := false
			for _, r := range s.Required {
				required = required || r == jsonName
			}
			if required && strings.Contains(opts, "omitempty") {
				t.Errorf("%s.%s is required but omitempty", name, jsonName)
			}
		}
		for prop := range s.Properties {
			if !fields[prop] {
				t.Errorf("%s.%s from openapi.json has no field", name, prop)
			}
		}
	}
	for name := range types {
		if _, ok := doc.Components.Schemas[name]; !ok {
			t.Errorf("wire type %s isn't in openapi.json", name)
		}
	}
}
