package validator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"sort"
	"strings"
)

// JSONValidator validates JSON files. When ForbidDuplicateKeys is true,
// duplicate keys in objects are reported as errors.
type JSONValidator struct {
	ForbidDuplicateKeys bool
}

var _ Validator = JSONValidator{}

func (v JSONValidator) ValidateSyntax(b []byte) (bool, error) {
	var output any
	err := json.Unmarshal(b, &output)
	if err != nil {
		var synErr *json.SyntaxError
		if errors.As(err, &synErr) {
			offset := int(synErr.Offset)
			line := 1 + strings.Count(string(b)[:offset], "\n")
			column := 1 + offset - (strings.LastIndex(string(b)[:offset], "\n") + len("\n"))
			return false, &ValidationError{
				Err:    synErr,
				Line:   line,
				Column: column,
			}
		}
		return false, err
	}

	if v.ForbidDuplicateKeys {
		if err := checkJSONDuplicateKeys(b); err != nil {
			return false, err
		}
	}

	return true, nil
}

func (JSONValidator) MarshalToJSON(b []byte) ([]byte, error) {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	if doc, ok := raw.(map[string]any); ok {
		delete(doc, "$schema")
		return json.Marshal(doc)
	}
	return b, nil
}

func (JSONValidator) ValidateSchema(b []byte, filePath string) (bool, error) {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
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

	return validateJSONSchema(schemaURL, cleanDoc, func(contexts []string) map[string]SourcePosition {
		return buildJSONPositionMap(b, contexts)
	})
}

// checkJSONDuplicateKeys walks the JSON token stream and reports duplicate keys.
func checkJSONDuplicateKeys(b []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	return checkDuplicateKeysInDecoder(dec)
}

// buildJSONPositionMap scans JSON bytes and returns the source position of
// each gojsonschema context path in wanted (e.g. "(root).name") that occurs
// in the document. It runs in time linear in len(b) and memory proportional
// to len(b) plus len(wanted): paths are tracked as incremental seeded hashes
// and only resolved to line and column when they match a wanted path.
func buildJSONPositionMap(b []byte, wanted []string) map[string]SourcePosition {
	positions := make(map[string]SourcePosition, len(wanted))
	if len(wanted) == 0 {
		return positions
	}

	seed := maphash.MakeSeed()
	wantedByHash := make(map[uint64]string, len(wanted))
	for _, w := range wanted {
		wantedByHash[maphash.String(seed, w)] = w
	}

	var newlines []int
	for i, c := range b {
		if c == '\n' {
			newlines = append(newlines, i)
		}
	}

	offsetToPos := func(offset int64) SourcePosition {
		off := min(int(offset), len(b))
		// Number of newlines strictly before off.
		n := sort.SearchInts(newlines, off)
		lastNL := -1
		if n > 0 {
			lastNL = newlines[n-1]
		}
		return SourcePosition{Line: 1 + n, Column: off - lastNL}
	}

	findKeyStart := func(endOffset int64) SourcePosition {
		for i := int(endOffset) - 1; i >= 0; i-- {
			if b[i] == '"' {
				for j := i - 1; j >= 0; j-- {
					if b[j] == '"' {
						return offsetToPos(int64(j))
					}
				}
			}
		}
		return offsetToPos(endOffset)
	}

	// Each frame stores the hash of its own path, so a child path costs only
	// the length of its key instead of the length of the full path.
	type frame struct {
		isArray bool
		path    maphash.Hash
	}

	var root maphash.Hash
	root.SetSeed(seed)
	root.WriteString("(root)")

	var stack []frame
	var pendingKey string
	expectingKey := false

	childPath := func(key string) maphash.Hash {
		h := root
		if len(stack) > 0 {
			h = stack[len(stack)-1].path
		}
		if key != "" {
			h.WriteString(".")
			h.WriteString(key)
		}
		return h
	}

	match := func(h *maphash.Hash) (string, bool) {
		p, ok := wantedByHash[h.Sum64()]
		return p, ok
	}

	recordAndReset := func() {
		pendingKey = ""
		expectingKey = len(stack) > 0 && !stack[len(stack)-1].isArray
	}

	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		afterOffset := dec.InputOffset()

		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				h := childPath(pendingKey)
				stack = append(stack, frame{isArray: false, path: h})
				pendingKey = ""
				expectingKey = true
				if p, ok := match(&h); ok {
					if _, exists := positions[p]; !exists {
						positions[p] = offsetToPos(afterOffset - 1)
					}
				}
			case '[':
				stack = append(stack, frame{isArray: true, path: childPath(pendingKey)})
				pendingKey = ""
			case '}', ']':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				expectingKey = len(stack) > 0 && !stack[len(stack)-1].isArray
			default:
			}
		case string:
			if expectingKey && len(stack) > 0 && !stack[len(stack)-1].isArray {
				pendingKey = v
				h := childPath(v)
				if p, ok := match(&h); ok {
					positions[p] = findKeyStart(afterOffset)
				}
				expectingKey = false
			} else {
				recordAndReset()
			}
		default:
			recordAndReset()
		}
	}

	return positions
}

func checkDuplicateKeysInDecoder(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return nil
	}

	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}

	if delim == '{' {
		return checkDuplicateKeysInObject(dec)
	}
	if delim == '[' {
		return checkDuplicateKeysInArray(dec)
	}
	return nil
}

func checkDuplicateKeysInObject(dec *json.Decoder) error {
	seen := make(map[string]struct{})
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		key, ok := tok.(string)
		if !ok {
			continue
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = struct{}{}

		// Consume the value — recurse to check nested objects
		if err := checkDuplicateKeysInDecoder(dec); err != nil {
			return err
		}
	}
	// consume closing }
	_, _ = dec.Token()
	return nil
}

func checkDuplicateKeysInArray(dec *json.Decoder) error {
	for dec.More() {
		if err := checkDuplicateKeysInDecoder(dec); err != nil {
			return err
		}
	}
	// consume closing ]
	_, _ = dec.Token()
	return nil
}
