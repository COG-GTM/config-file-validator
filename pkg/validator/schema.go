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

// positionLocator returns source positions for the given gojsonschema
// context strings. It is only called when validation fails.
type positionLocator func(contexts []string) map[string]SourcePosition

// JSONSchemaValidateWithPositions validates docJSON against schemaURL and
// annotates errors with source positions from posMap. The map keys are
// gojsonschema context strings like "(root).name".
func JSONSchemaValidateWithPositions(schemaURL string, docJSON []byte, posMap map[string]SourcePosition) (bool, error) {
	if posMap == nil {
		return validateJSONSchema(schemaURL, docJSON, nil)
	}
	return validateJSONSchema(schemaURL, docJSON, func([]string) map[string]SourcePosition { return posMap })
}

func validateJSONSchema(schemaURL string, docJSON []byte, locate positionLocator) (bool, error) {
	schemaLoader := gojsonschema.NewReferenceLoader(schemaURL)
	documentLoader := gojsonschema.NewBytesLoader(docJSON)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return false, fmt.Errorf("schema validation error: %w", err)
	}

	if !result.Valid() {
		resultErrs := result.Errors()
		contexts := make([]string, len(resultErrs))
		for i, desc := range resultErrs {
			contexts[i] = desc.Context().String()
		}
		var posMap map[string]SourcePosition
		if locate != nil {
			posMap = locate(contexts)
		}
		var errs []string
		var positions []SchemaErrorPosition
		for i, desc := range resultErrs {
			errs = append(errs, desc.String())
			var pos SchemaErrorPosition
			if sp, ok := posMap[contexts[i]]; ok {
				pos = SchemaErrorPosition(sp)
			}
			positions = append(positions, pos)
		}
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
