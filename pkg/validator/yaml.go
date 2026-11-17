package validator

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/xeipuuv/gojsonschema"
	"gopkg.in/yaml.v3"
)

// YAMLValidator validates YAML files.
// Note: yaml.v3 already rejects duplicate keys by default.
type YAMLValidator struct{}

var _ Validator = YAMLValidator{}

var yamlLineRe = regexp.MustCompile(`yaml: line (\d+): (.*)`)

func (YAMLValidator) ValidateSyntax(b []byte) (bool, error) {
	var output any
	err := yaml.Unmarshal(b, &output)
	if err != nil {
		if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
			if line, convErr := strconv.Atoi(m[1]); convErr == nil {
				return false, &ValidationError{Err: errors.New(m[2]), Line: line}
			}
		}
		return false, err
	}

	return true, nil
}

func (YAMLValidator) MarshalToJSON(b []byte) ([]byte, error) {
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

func (YAMLValidator) ValidateSchema(b []byte, filePath string) (bool, error) {
	schemaURL := extractYAMLSchemaComment(b)
	if schemaURL == "" {
		return true, ErrNoSchema
	}

	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return false, err
	}

	docJSON, err := json.Marshal(doc)
	if err != nil {
		return false, err
	}

	return validateJSONSchema(resolveSchemaURL(schemaURL, filePath), docJSON, newYAMLPositionLookup(b))
}

// extractYAMLSchemaComment scans for the yaml-language-server schema modeline:
//
//	# yaml-language-server: $schema=<url>
func extractYAMLSchemaComment(b []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(b))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			return ""
		}
		const prefix = "yaml-language-server:"
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[idx+len(prefix):])
		if after, ok := strings.CutPrefix(rest, "$schema="); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// yamlContextSep splits gojsonschema context paths into segments. NUL is used
// instead of "." so that keys containing dots resolve correctly.
const yamlContextSep = "\x00"

// newYAMLPositionLookup resolves schema error contexts (e.g. "(root).server.port")
// to source positions by walking the YAML node tree along each error path.
// The tree is parsed only when the first error needs a position, and no
// per-node path strings are stored, so memory stays linear in the input size.
func newYAMLPositionLookup(b []byte) positionLookup {
	var root *yaml.Node
	parsed := false
	return func(ctx *gojsonschema.JsonContext) (SourcePosition, bool) {
		if !parsed {
			parsed = true
			var doc yaml.Node
			if err := yaml.Unmarshal(b, &doc); err == nil && doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
				root = doc.Content[0]
			}
		}
		if root == nil || ctx == nil {
			return SourcePosition{}, false
		}
		return findYAMLPosition(root, strings.Split(ctx.String(yamlContextSep), yamlContextSep))
	}
}

// findYAMLPosition walks root along path, whose first segment is "(root)".
// Mapping and sequence values report the position of their first entry;
// scalar values report the position of their key (or sequence item).
func findYAMLPosition(root *yaml.Node, path []string) (SourcePosition, bool) {
	if len(path) == 0 || path[0] != "(root)" {
		return SourcePosition{}, false
	}
	if root.Kind != yaml.MappingNode && root.Kind != yaml.SequenceNode {
		return SourcePosition{}, false
	}
	node := root
	pos := SourcePosition{Line: root.Line, Column: root.Column}
	for _, seg := range path[1:] {
		var anchor, child *yaml.Node
		switch node.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == seg {
					anchor, child = node.Content[i], node.Content[i+1]
					break
				}
			}
		case yaml.SequenceNode:
			if idx, err := strconv.Atoi(seg); err == nil && idx >= 0 && idx < len(node.Content) {
				anchor, child = node.Content[idx], node.Content[idx]
			}
		default:
		}
		if child == nil {
			return SourcePosition{}, false
		}
		if child.Kind == yaml.MappingNode || child.Kind == yaml.SequenceNode {
			anchor = child
		}
		pos = SourcePosition{Line: anchor.Line, Column: anchor.Column}
		node = child
	}
	return pos, true
}
