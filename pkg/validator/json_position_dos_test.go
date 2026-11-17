package validator

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// allocated returns the bytes allocated and the time taken by f.
func allocated(f func()) (uint64, time.Duration) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	f()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc, elapsed
}

// wideJSONObject returns a single-line object with n short integer keys
// followed by one string key "bad".
func wideJSONObject(n int) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"$schema":"schema.json"`)
	for i := range n {
		buf.WriteString(`,"k`)
		buf.WriteString(strconv.Itoa(i))
		buf.WriteString(`":1`)
	}
	buf.WriteString(`,"bad":"v"}`)
	return buf.Bytes()
}

// deepJSONObject returns depth nested objects under keys of keyLen bytes,
// with leaves integer keys and one string key "bad" in the innermost object.
func deepJSONObject(depth, keyLen, leaves int) []byte {
	key := strings.Repeat("a", keyLen)
	var buf bytes.Buffer
	buf.WriteString(`{"$schema":"schema.json"`)
	for range depth {
		buf.WriteString(`,"` + key + `":{"x":0`)
	}
	for i := range leaves {
		buf.WriteString(`,"l`)
		buf.WriteString(strconv.Itoa(i))
		buf.WriteString(`":0`)
	}
	buf.WriteString(`,"bad":"v"`)
	buf.WriteString(strings.Repeat("}", depth+1))
	return buf.Bytes()
}

// Regression test for the JSON position map DoS (sfind-a838c63a): building
// source positions for a document that declares $schema must not cost time
// proportional to key count times file size, nor memory proportional to
// nesting depth times key length times key count.
func TestJSONValidateSchemaPositionMapBounded(t *testing.T) {
	permissive := `{}`
	// Only the "bad" string leaf fails, so exactly one position is resolved.
	failing := `{"type":"object","additionalProperties":{"$ref":"#/definitions/node"},` +
		`"definitions":{"node":{"type":["object","integer"],"additionalProperties":{"$ref":"#/definitions/node"}}}}`

	cases := []struct {
		name   string
		schema string
		doc    []byte
	}{
		{"wide object, valid", permissive, wideJSONObject(15000)},
		{"wide object, one error", failing, wideJSONObject(15000)},
		{"deep object, valid", permissive, deepJSONObject(500, 100, 3000)},
		{"deep object, one error", failing, deepJSONObject(500, 100, 3000)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			schemaPath := filepath.Join(dir, "schema.json")
			require.NoError(t, os.WriteFile(schemaPath, []byte(tc.schema), 0o600))
			docPath := filepath.Join(dir, "doc.json")

			var doc map[string]any
			require.NoError(t, json.Unmarshal(tc.doc, &doc))
			delete(doc, "$schema")
			cleanDoc, err := json.Marshal(doc)
			require.NoError(t, err)
			schemaURL := resolveSchemaURL("schema.json", docPath)

			// Allocations made by gojsonschema itself are not under test.
			baseline, _ := allocated(func() { _, _ = JSONSchemaValidate(schemaURL, cleanDoc) })

			var valid bool
			var validateErr error
			total, elapsed := allocated(func() { valid, validateErr = JSONValidator{}.ValidateSchema(tc.doc, docPath) })
			overhead := int64(total) - int64(baseline)
			t.Logf("input=%d bytes gojsonschema=%d bytes overhead=%d bytes elapsed=%s",
				len(tc.doc), baseline, overhead, elapsed)

			if tc.schema == permissive {
				require.True(t, valid)
				require.NoError(t, validateErr)
			} else {
				require.False(t, valid)
				var schemaErrs *SchemaErrors
				require.ErrorAs(t, validateErr, &schemaErrs)
				require.Len(t, schemaErrs.Positions, 1)
				col := bytes.Index(tc.doc, []byte(`"bad"`)) + 1
				require.Equal(t, SchemaErrorPosition{Line: 1, Column: col}, schemaErrs.Positions[0])
			}
			require.Less(t, overhead, int64(100*len(tc.doc)), "position tracking must allocate in proportion to the input")
			require.Less(t, elapsed, 10*time.Second)
		})
	}
}
