package validator

import (
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/xeipuuv/gojsonschema"

	"github.com/Boeing/config-file-validator/v2/pkg/tools"
)

// SourcePosition holds a 1-based line and column in the original source file.
type SourcePosition struct {
	Line   int
	Column int
}

func JSONSchemaValidate(schemaURL string, docJSON []byte) (bool, error) {
	return validateJSONSchema(schemaURL, docJSON, nil)
}

// JSONSchemaValidateWithPositions validates docJSON against schemaURL and
// annotates errors with source positions from posMap. The map keys are
// gojsonschema context strings like "(root).name".
func JSONSchemaValidateWithPositions(schemaURL string, docJSON []byte, posMap map[string]SourcePosition) (bool, error) {
	return validateJSONSchema(schemaURL, docJSON, posMap)
}

func validateJSONSchema(schemaURL string, docJSON []byte, posMap map[string]SourcePosition) (bool, error) {
	return JSONSchemaValidateDocuments(schemaURL, [][]byte{docJSON}, []map[string]SourcePosition{posMap})
}

// JSONSchemaValidateDocuments validates every JSON document in docs against
// schemaURL. posMaps is optional; when set, posMaps[i] annotates errors for
// docs[i]. Errors from all documents are combined, and for multi-document
// input each error is prefixed with its 1-based document number.
func JSONSchemaValidateDocuments(schemaURL string, docs [][]byte, posMaps []map[string]SourcePosition) (bool, error) {
	schema, err := gojsonschema.NewSchema(gojsonschema.NewReferenceLoader(schemaURL))
	if err != nil {
		return false, fmt.Errorf("schema validation error: %w", err)
	}

	var errs []string
	var positions []SchemaErrorPosition
	for i, docJSON := range docs {
		result, err := schema.Validate(gojsonschema.NewBytesLoader(docJSON))
		if err != nil {
			return false, fmt.Errorf("schema validation error: %w", err)
		}

		var posMap map[string]SourcePosition
		if i < len(posMaps) {
			posMap = posMaps[i]
		}
		for _, desc := range result.Errors() {
			msg := desc.String()
			if len(docs) > 1 {
				msg = fmt.Sprintf("document %d: %s", i+1, msg)
			}
			errs = append(errs, msg)
			var pos SchemaErrorPosition
			if sp, ok := posMap[desc.Context().String()]; ok {
				pos = SchemaErrorPosition(sp)
			}
			positions = append(positions, pos)
		}
	}

	if len(errs) > 0 {
		return false, &SchemaErrors{Prefix: "schema validation failed: ", Items: errs, Positions: positions}
	}

	return true, nil
}

func resolveSchemaURL(schemaURL, filePath string) string {
	if filepath.IsAbs(schemaURL) {
		return tools.FileURL(schemaURL)
	}

	parsed, err := url.Parse(schemaURL)
	if err == nil && parsed.Scheme != "" {
		return schemaURL
	}

	dir := filepath.Dir(filePath)
	absSchema, err := filepath.Abs(filepath.Join(dir, schemaURL))
	if err != nil {
		return schemaURL
	}
	return tools.FileURL(absSchema)
}
