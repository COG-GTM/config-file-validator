package validator

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test_YAMLValidateSchemaDeepNestingMemory guards against quadratic memory use
// when mapping schema errors back to YAML source positions. A ~500 KB document
// nested 5,000 levels deep with 100-character keys used to retain over 1 GB of
// path strings. It is not parallel because it measures process-wide
// allocations.
func Test_YAMLValidateSchemaDeepNestingMemory(t *testing.T) {
	const (
		depth    = 5000
		maxAlloc = 256 << 20
	)

	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	require.NoError(t, os.WriteFile(schemaPath, []byte(`{"required": ["missing"]}`), 0o600))

	key := strings.Repeat("k", 100)
	var sb strings.Builder
	sb.WriteString("# yaml-language-server: $schema=schema.json\n")
	for range depth {
		sb.WriteString("{" + key + ": ")
	}
	sb.WriteString("leaf")
	sb.WriteString(strings.Repeat("}", depth))
	sb.WriteString("\n")
	doc := []byte(sb.String())

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	valid, err := YAMLValidator{}.ValidateSchema(doc, filepath.Join(dir, "deep.yaml"))

	runtime.ReadMemStats(&after)

	require.False(t, valid)
	var schemaErrs *SchemaErrors
	require.ErrorAs(t, err, &schemaErrs)
	require.Len(t, schemaErrs.Positions, 1)
	require.Equal(t, 2, schemaErrs.Positions[0].Line)

	allocated := after.TotalAlloc - before.TotalAlloc
	require.Lessf(t, allocated, uint64(maxAlloc),
		"ValidateSchema allocated %d MB for a %d KB document", allocated>>20, len(doc)>>10)
}
