package validator

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeSchemaFixture writes doc next to a schema file and returns the doc path.
func writeSchemaFixture(t *testing.T, schema string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.json"), []byte(schema), 0o600))
	return filepath.Join(dir, "doc.json")
}

// measureValidateSchema runs ValidateSchema and returns the bytes allocated
// and the elapsed time.
func measureValidateSchema(t *testing.T, doc []byte, docPath string) (bool, error, uint64, time.Duration) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	valid, err := JSONValidator{}.ValidateSchema(doc, docPath)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	return valid, err, after.TotalAlloc - before.TotalAlloc, elapsed
}

// wideJSONObject returns a single-line object with n short keys after $schema.
func wideJSONObject(n int) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"$schema":"schema.json"`)
	for i := range n {
		buf.WriteString(`,"k`)
		buf.WriteString(strconv.Itoa(i))
		buf.WriteString(`":1`)
	}
	buf.WriteString("}")
	return buf.Bytes()
}

// deepJSONObject returns depth nested objects under long keys with leaves
// short keys in the innermost object.
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
		buf.WriteString(`":"v"`)
	}
	buf.WriteString(strings.Repeat("}", depth+1))
	return buf.Bytes()
}

// Regression test for the JSON position map DoS: a document that declares
// $schema must not cost time quadratic in key count times file size, nor
// memory quadratic in nesting depth times key length.
func TestJSONValidateSchemaPositionMapBounded(t *testing.T) {
	permissive := `{}`
	// Every leaf string fails, so positions are needed for each deep leaf.
	failing := `{"type":"object","additionalProperties":{"$ref":"#/definitions/node"},` +
		`"definitions":{"node":{"type":["object","integer"],"additionalProperties":{"$ref":"#/definitions/node"}}}}`

	cases := []struct {
		name   string
		schema string
		doc    []byte
		valid  bool
	}{
		{"wide object, valid", permissive, wideJSONObject(60000), true},
		{"wide object, typed schema", failing, wideJSONObject(60000), true},
		{"deep object, valid", permissive, deepJSONObject(800, 100, 4000), true},
		{"deep object, errors", failing, deepJSONObject(800, 100, 4000), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docPath := writeSchemaFixture(t, tc.schema)
			valid, err, allocated, elapsed := measureValidateSchema(t, tc.doc, docPath)
			t.Logf("input=%d bytes allocated=%d bytes elapsed=%s", len(tc.doc), allocated, elapsed)
			require.Equal(t, tc.valid, valid, "err: %v", err)
			if !tc.valid {
				var schemaErrs *SchemaErrors
				require.ErrorAs(t, err, &schemaErrs)
				require.NotZero(t, schemaErrs.Positions[0].Line)
			}
			require.Less(t, allocated, uint64(200*len(tc.doc)), "allocations must stay proportional to input size")
			require.Less(t, elapsed, 10*time.Second)
		})
	}
}
