package yamlcfg

import (
	"strings"
	"testing"
)

const schemaText = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "name": {"type": "string"},
    "rate": {"type": "integer", "minimum": 1},
    "when": {"type": "string"},
    "items": {"type": "array", "items": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}}}}
  }
}`

var schema = MustCompile("test.schema.json", schemaText)

type cfg struct {
	Name  string `yaml:"name"`
	Rate  int    `yaml:"rate"`
	When  string `yaml:"when"`
	Items []struct {
		ID string `yaml:"id"`
	} `yaml:"items"`
}

func TestDecodeValid(t *testing.T) {
	var c cfg
	if err := schema.Decode([]byte("name: x\nrate: 5\nitems:\n  - id: a\n"), &c); err != nil {
		t.Fatal(err)
	}
	if c.Name != "x" || c.Rate != 5 || len(c.Items) != 1 || c.Items[0].ID != "a" {
		t.Errorf("decoded %+v", c)
	}
}

func TestDecodeEmptyDocumentIsEmptyObject(t *testing.T) {
	var c cfg
	if err := schema.Decode([]byte("# only a comment\n"), &c); err != nil {
		t.Fatalf("empty file should validate as {}: %v", err)
	}
}

func TestDecodeReportsLeafLocation(t *testing.T) {
	cases := map[string]string{
		"rate: 0\n":                "/rate",
		"unknown: 1\n":             "additionalProperties",
		"items:\n  - {}\n":         "/items/0",
		"name: [not, a, string]\n": "/name",
		"name: \"unterminated\n":   "yaml",
		"rate: 1\nrate: 2\n":       "rate",
	}
	for text, want := range cases {
		var c cfg
		err := schema.Decode([]byte(text), &c)
		if err == nil {
			t.Errorf("%q: accepted", text)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %q should mention %q", text, err, want)
		}
	}
}

func TestDecodeIntegerFloatAndTimestampScalars(t *testing.T) {
	// yaml.v3 decodes 5 as int, 5.0 as float64 and dates as time.Time; the
	// validator must see JSON-compatible values for all of them.
	var c cfg
	if err := schema.Decode([]byte("rate: 5\nwhen: 2026-10-19T12:00:00Z\n"), &c); err != nil {
		t.Fatalf("int and timestamp: %v", err)
	}
	if err := schema.Decode([]byte("rate: 5.5\n"), &c); err == nil {
		t.Error("5.5 accepted for an integer")
	}
}

func TestMustCompilePanicsOnBadSchema(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("bad schema did not panic")
		}
	}()
	MustCompile("bad.json", `{"type": 42}`)
}
