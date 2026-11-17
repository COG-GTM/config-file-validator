package validator

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAMLValidator validates YAML files.
// Note: yaml.v3 already rejects duplicate keys by default.
type YAMLValidator struct{}

var _ Validator = YAMLValidator{}

var yamlLineRe = regexp.MustCompile(`yaml: line (\d+): (.*)`)

// ErrMultipleYAMLDocuments is returned by YAMLValidator.MarshalToJSON when
// the input contains more than one YAML document. Use MarshalDocumentsToJSON
// to convert every document.
var ErrMultipleYAMLDocuments = errors.New("yaml: input contains multiple documents; use MarshalDocumentsToJSON")

type yamlDocument struct {
	value any
	node  *yaml.Node
}

// decodeYAMLDocuments decodes every document in a YAML stream. yaml.Unmarshal
// stops after the first document, so later documents would otherwise never be
// checked.
func decodeYAMLDocuments(b []byte) ([]yamlDocument, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var docs []yamlDocument
	for {
		node := &yaml.Node{}
		err := dec.Decode(node)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		var value any
		if err := node.Decode(&value); err != nil {
			return nil, err
		}
		docs = append(docs, yamlDocument{value: value, node: node})
	}
}

func (YAMLValidator) ValidateSyntax(b []byte) (bool, error) {
	if _, err := decodeYAMLDocuments(b); err != nil {
		if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
			if line, convErr := strconv.Atoi(m[1]); convErr == nil {
				return false, &ValidationError{Err: errors.New(m[2]), Line: line}
			}
		}
		return false, err
	}

	return true, nil
}

// MarshalToJSON converts a single-document YAML file to JSON. It returns
// ErrMultipleYAMLDocuments for multi-document input so callers cannot
// silently validate only the first document.
func (v YAMLValidator) MarshalToJSON(b []byte) ([]byte, error) {
	docs, err := v.MarshalDocumentsToJSON(b)
	if err != nil {
		return nil, err
	}
	if len(docs) > 1 {
		return nil, ErrMultipleYAMLDocuments
	}
	return docs[0], nil
}

// MarshalDocumentsToJSON converts every document in a YAML stream to JSON.
// Input with no documents yields a single JSON null.
func (YAMLValidator) MarshalDocumentsToJSON(b []byte) ([][]byte, error) {
	docs, err := decodeYAMLDocuments(b)
	if err != nil {
		return nil, err
	}
	return yamlDocumentsToJSON(docs)
}

func yamlDocumentsToJSON(docs []yamlDocument) ([][]byte, error) {
	if len(docs) == 0 {
		return [][]byte{[]byte("null")}, nil
	}
	out := make([][]byte, 0, len(docs))
	for _, doc := range docs {
		docJSON, err := json.Marshal(doc.value)
		if err != nil {
			return nil, err
		}
		out = append(out, docJSON)
	}
	return out, nil
}

func (YAMLValidator) ValidateSchema(b []byte, filePath string) (bool, error) {
	schemaURL := extractYAMLSchemaComment(b)
	if schemaURL == "" {
		return true, ErrNoSchema
	}

	docs, err := decodeYAMLDocuments(b)
	if err != nil {
		return false, err
	}

	docsJSON, err := yamlDocumentsToJSON(docs)
	if err != nil {
		return false, err
	}

	posMaps := make([]map[string]SourcePosition, len(docs))
	for i, doc := range docs {
		posMaps[i] = yamlPositionMap(doc.node)
	}
	return JSONSchemaValidateDocuments(resolveSchemaURL(schemaURL, filePath), docsJSON, posMaps)
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

// yamlPositionMap builds a map from gojsonschema context paths
// (e.g. "(root).server.port") to source positions for one YAML document.
func yamlPositionMap(doc *yaml.Node) map[string]SourcePosition {
	positions := make(map[string]SourcePosition)
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		walkYAMLNode(doc.Content[0], "(root)", positions)
	}
	return positions
}

func walkYAMLNode(node *yaml.Node, path string, positions map[string]SourcePosition) {
	switch node.Kind {
	case yaml.MappingNode:
		positions[path] = SourcePosition{Line: node.Line, Column: node.Column}
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valNode := node.Content[i+1]
			childPath := path + "." + keyNode.Value
			positions[childPath] = SourcePosition{Line: keyNode.Line, Column: keyNode.Column}
			walkYAMLNode(valNode, childPath, positions)
		}
	case yaml.SequenceNode:
		positions[path] = SourcePosition{Line: node.Line, Column: node.Column}
		for i, child := range node.Content {
			childPath := fmt.Sprintf("%s.%d", path, i)
			positions[childPath] = SourcePosition{Line: child.Line, Column: child.Column}
			walkYAMLNode(child, childPath, positions)
		}
	default:
	}
}
