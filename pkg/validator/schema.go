package validator

import (
	"fmt"

	"github.com/xeipuuv/gojsonschema"
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
	return validateWithLoader(gojsonschema.NewReferenceLoader(schemaURL), docJSON, posMap)
}

func validateWithLoader(schemaLoader gojsonschema.JSONLoader, docJSON []byte, posMap map[string]SourcePosition) (bool, error) {
	documentLoader := gojsonschema.NewBytesLoader(docJSON)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return false, fmt.Errorf("schema validation error: %w", err)
	}

	if !result.Valid() {
		var errs []string
		var positions []SchemaErrorPosition
		for _, desc := range result.Errors() {
			errs = append(errs, desc.String())
			var pos SchemaErrorPosition
			if posMap != nil {
				if sp, ok := posMap[desc.Context().String()]; ok {
					pos = SchemaErrorPosition(sp)
				}
			}
			positions = append(positions, pos)
		}
		return false, &SchemaErrors{Prefix: "schema validation failed: ", Items: errs, Positions: positions}
	}

	return true, nil
}
