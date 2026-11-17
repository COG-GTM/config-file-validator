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

// positionLookup returns the source position for a gojsonschema error context.
type positionLookup func(ctx *gojsonschema.JsonContext) (SourcePosition, bool)

func JSONSchemaValidate(schemaURL string, docJSON []byte) (bool, error) {
	return validateJSONSchema(schemaURL, docJSON, nil)
}

// JSONSchemaValidateWithPositions validates docJSON against schemaURL and
// annotates errors with source positions from posMap. The map keys are
// gojsonschema context strings like "(root).name".
func JSONSchemaValidateWithPositions(schemaURL string, docJSON []byte, posMap map[string]SourcePosition) (bool, error) {
	var lookup positionLookup
	if posMap != nil {
		lookup = func(ctx *gojsonschema.JsonContext) (SourcePosition, bool) {
			sp, ok := posMap[ctx.String()]
			return sp, ok
		}
	}
	return validateJSONSchema(schemaURL, docJSON, lookup)
}

func validateJSONSchema(schemaURL string, docJSON []byte, lookup positionLookup) (bool, error) {
	schemaLoader := gojsonschema.NewReferenceLoader(schemaURL)
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
			if lookup != nil {
				if sp, ok := lookup(desc.Context()); ok {
					pos = SchemaErrorPosition(sp)
				}
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
