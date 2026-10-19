// Package yamlcfg loads YAML config files validated by a JSON Schema
// committed next to each loader. Invalid config never crashes a caller: Load
// returns the error and callers keep their last-good value.
package yamlcfg

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"
)

// Schema is a compiled JSON Schema.
type Schema struct{ s *jsonschema.Schema }

// MustCompile compiles schema text (panics on a bad schema: it ships in the binary).
func MustCompile(name, text string) *Schema {
	c := jsonschema.NewCompiler()
	if err := c.AddResource(name, strings.NewReader(text)); err != nil {
		panic(err)
	}
	s, err := c.Compile(name)
	if err != nil {
		panic(err)
	}
	return &Schema{s}
}

// Decode validates data against the schema and decodes it into out.
func (s *Schema) Decode(data []byte, out any) error {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	if err := s.s.Validate(jsonCompatible(doc)); err != nil {
		if ve, ok := err.(*jsonschema.ValidationError); ok {
			leaf := ve
			for len(leaf.Causes) > 0 {
				leaf = leaf.Causes[0]
			}
			return fmt.Errorf("%s: %s", leaf.InstanceLocation, leaf.Message)
		}
		return err
	}
	return yaml.Unmarshal(data, out)
}

func jsonCompatible(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, vv := range x {
			out[k] = jsonCompatible(vv)
		}
		return out
	case map[any]any:
		out := map[string]any{}
		for k, vv := range x {
			out[fmt.Sprint(k)] = jsonCompatible(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = jsonCompatible(vv)
		}
		return out
	case int:
		return json.Number(fmt.Sprint(x))
	case int64:
		return json.Number(fmt.Sprint(x))
	case float64:
		return json.Number(fmt.Sprint(x))
	case time.Time:
		return x.Format(time.RFC3339)
	}
	return v
}
