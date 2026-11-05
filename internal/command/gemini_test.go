package command

import (
	"testing"

	"google.golang.org/genai"
)

// Gemini rejects a response schema with an empty enum value (found on the
// first live command, 2026-11-05).
func TestRawSchemaHasNoEmptyEnumValues(t *testing.T) {
	var walk func(path string, s *genai.Schema)
	walk = func(path string, s *genai.Schema) {
		if s == nil {
			return
		}
		for _, v := range s.Enum {
			if v == "" {
				t.Errorf("%s: empty enum value", path)
			}
		}
		for k, p := range s.Properties {
			walk(path+"."+k, p)
		}
		walk(path+"[]", s.Items)
	}
	walk("schema", rawSchema())
}
