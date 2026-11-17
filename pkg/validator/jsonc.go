package validator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tailscale/hujson"
)

type JSONCValidator struct{}

var _ Validator = JSONCValidator{}

// maxJSONCDepth matches the nesting limit enforced by encoding/json.
const maxJSONCDepth = 10000

func (JSONCValidator) ValidateSyntax(b []byte) (bool, error) {
	if err := checkJSONCDepth(b); err != nil {
		return false, err
	}
	_, err := hujson.Parse(b)
	if err != nil {
		line, col := parseHujsonError(err)
		if line > 0 {
			return false, &ValidationError{
				Err:    err,
				Line:   line,
				Column: col,
			}
		}
		return false, err
	}
	return true, nil
}

func (JSONCValidator) MarshalToJSON(b []byte) ([]byte, error) {
	if err := checkJSONCDepth(b); err != nil {
		return nil, err
	}
	standardized, err := hujson.Standardize(b)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(standardized, &raw); err != nil {
		return nil, err
	}
	if doc, ok := raw.(map[string]any); ok {
		delete(doc, "$schema")
		return json.Marshal(doc)
	}
	return standardized, nil
}

func (JSONCValidator) ValidateSchema(b []byte, filePath string) (bool, error) {
	if err := checkJSONCDepth(b); err != nil {
		return false, err
	}
	standardized, err := hujson.Standardize(b)
	if err != nil {
		return false, err
	}

	var raw any
	if err := json.Unmarshal(standardized, &raw); err != nil {
		return false, err
	}

	doc, ok := raw.(map[string]any)
	if !ok {
		return true, ErrNoSchema
	}

	schemaRef, ok := doc["$schema"]
	if !ok {
		return true, ErrNoSchema
	}

	schemaURL, ok := schemaRef.(string)
	if !ok {
		return false, fmt.Errorf("$schema must be a string, got %T", schemaRef)
	}
	if schemaURL == "" {
		return false, errors.New("$schema must not be empty")
	}

	schemaURL = resolveSchemaURL(schemaURL, filePath)

	delete(doc, "$schema")
	cleanDoc, err := json.Marshal(doc)
	if err != nil {
		return false, err
	}

	return JSONSchemaValidate(schemaURL, cleanDoc)
}

// checkJSONCDepth rejects input nested deeper than maxJSONCDepth before it
// reaches hujson, whose parser recurses once per nesting level. Brackets
// inside strings and comments are not counted.
func checkJSONCDepth(b []byte) error {
	depth := 0
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '"':
			for i++; i < len(b) && b[i] != '"'; i++ {
				if b[i] == '\\' {
					i++
				}
			}
		case '/':
			if i+1 >= len(b) {
				continue
			}
			switch b[i+1] {
			case '/':
				if j := bytes.IndexByte(b[i:], '\n'); j >= 0 {
					i += j
				} else {
					i = len(b)
				}
			case '*':
				if j := bytes.Index(b[i+2:], []byte("*/")); j >= 0 {
					i += j + 3
				} else {
					i = len(b)
				}
			}
		case '[', '{':
			depth++
			if depth > maxJSONCDepth {
				line := 1 + bytes.Count(b[:i], []byte("\n"))
				col := 1 + i - (bytes.LastIndexByte(b[:i], '\n') + 1)
				return &ValidationError{
					Err:    fmt.Errorf("exceeded max depth of %d", maxJSONCDepth),
					Line:   line,
					Column: col,
				}
			}
		case ']', '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return nil
}

// parseHujsonError extracts line and column from hujson error messages.
// hujson errors look like: "line 3, column 5: ..."
func parseHujsonError(err error) (line int, col int) {
	msg := err.Error()
	if n, _ := fmt.Sscanf(msg, "line %d, column %d", &line, &col); n >= 1 {
		return line, col
	}
	if strings.Contains(msg, "line ") {
		if n, _ := fmt.Sscanf(msg, "line %d", &line); n >= 1 {
			return line, 0
		}
	}
	return 0, 0
}
