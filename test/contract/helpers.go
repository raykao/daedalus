package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const schemasDir = "schemas"

// newCompiler creates a compiler pre-loaded with all schemas from the schemas directory.
// Each schema's $id is used as its resource URL, enabling cross-schema $ref resolution.
func newCompiler() (*jsonschema.Compiler, error) {
	c := jsonschema.NewCompiler()

	entries, err := os.ReadDir(schemasDir)
	if err != nil {
		return nil, fmt.Errorf("reading schemas dir: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(schemasDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading schema %s: %w", e.Name(), err)
		}

		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parsing schema %s: %w", e.Name(), err)
		}

		obj, ok := doc.(map[string]any)
		if !ok {
			continue
		}

		id, _ := obj["$id"].(string)
		if id == "" {
			continue
		}

		if err := c.AddResource(id, doc); err != nil {
			return nil, fmt.Errorf("adding schema resource %s: %w", id, err)
		}
	}

	return c, nil
}

// loadSchema loads and compiles a JSON Schema by filename from the schemas directory.
func loadSchema(name string) (*jsonschema.Schema, error) {
	c, err := newCompiler()
	if err != nil {
		return nil, err
	}
	id := "https://daedalus/schemas/" + name
	return c.Compile(id)
}

// validateJSON validates a JSON document against a compiled schema.
func validateJSON(schema *jsonschema.Schema, data []byte) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("parsing JSON: %w", err)
	}
	return schema.Validate(v)
}

// mustMarshal marshals a Go value to JSON, panicking on error.
func mustMarshal(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustMarshal: %v", err))
	}
	return data
}
