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
	schema, err := loadSchema(schemaURL)
	if err != nil {
		return false, fmt.Errorf("schema validation error: %w", err)
	}

	result, err := schema.Validate(gojsonschema.NewBytesLoader(docJSON))
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

// loadSchema compiles the schema at schemaURL. Remote HTTP(S) schemas are
// downloaded with a timeout and size cap before being handed to gojsonschema,
// which otherwise reads them without either limit.
func loadSchema(schemaURL string) (*gojsonschema.Schema, error) {
	root := gojsonschema.NewReferenceLoader(schemaURL)
	parsed, err := url.Parse(schemaURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return gojsonschema.NewSchema(root)
	}

	body, err := tools.FetchRemoteSchema(schemaURL)
	if err != nil {
		return nil, err
	}
	loader := gojsonschema.NewSchemaLoader()
	if err := loader.AddSchema(schemaURL, gojsonschema.NewBytesLoader(body)); err != nil {
		return nil, err
	}
	return loader.Compile(root)
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
