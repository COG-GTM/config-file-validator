package validator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const schemaLocationSecret = "TOKEN-do-not-leak-0123456789"

func xmlWithSchemaLocation(loc string) []byte {
	return []byte(`<?xml version="1.0"?>
<config xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
        xsi:noNamespaceSchemaLocation="` + loc + `">
  <host>db.example.com</host>
  <port>5432</port>
</config>`)
}

func writeSchemaFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	xsd := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<xs:schema xmlns:xs="` + xsNamespace + `">` + // DevSkim: ignore DS137138
		`<xs:element name="config"><xs:complexType><xs:sequence>` +
		`<xs:element name="host" type="xs:string"/>` +
		`<xs:element name="port" type="xs:integer"/>` +
		`</xs:sequence></xs:complexType></xs:element></xs:schema>`
	require.NoError(t, os.WriteFile(path, []byte(xsd), 0o600))
}

// schemaLocationFixture lays out:
//
//	base/outside.xsd          valid schema outside the scanned root
//	base/secret.xsd           one-line "secret" outside the root
//	base/root/doc.xml         the scanned document (not written; only its path is used)
//	base/root/schema.xsd      valid schema inside the root
//	base/root/schemas/s.xsd   valid schema in a subdirectory
//	base/root/notes.txt       non-schema file inside the root
func schemaLocationFixture(t *testing.T) (base, root string) {
	t.Helper()
	base = t.TempDir()
	root = filepath.Join(base, "root")
	writeSchemaFile(t, filepath.Join(base, "outside.xsd"))
	require.NoError(t, os.WriteFile(filepath.Join(base, "secret.xsd"), []byte(schemaLocationSecret+"\n"), 0o600))
	writeSchemaFile(t, filepath.Join(root, "schema.xsd"))
	writeSchemaFile(t, filepath.Join(root, "schemas", "s.xsd"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte(schemaLocationSecret+"\n"), 0o600))
	return base, root
}

func Test_XMLSchemaLocationRejectsEscapes(t *testing.T) {
	t.Parallel()
	base, root := schemaLocationFixture(t)

	require.NoError(t, os.Symlink(filepath.Join(base, "outside.xsd"), filepath.Join(root, "escape.xsd")))
	require.NoError(t, os.Symlink(filepath.Join(base, "secret.xsd"), filepath.Join(root, "secret-link.xsd")))
	require.NoError(t, os.Symlink(filepath.Join(root, "notes.txt"), filepath.Join(root, "notes.xsd")))
	require.NoError(t, os.Mkdir(filepath.Join(root, "dir.xsd"), 0o750))
	big, err := os.Create(filepath.Join(root, "big.xsd"))
	require.NoError(t, err)
	require.NoError(t, big.Truncate(maxXSDSize+1))
	require.NoError(t, big.Close())

	tests := []struct {
		name    string
		loc     string
		wantErr string
	}{
		{"absolute path to existing schema", filepath.Join(base, "outside.xsd"), "must be a relative path"},
		{"absolute system file", "/etc/hostname", "must be a relative path"},
		{"absolute dev zero", "/dev/zero", "must be a relative path"},
		{"file URL", "file://" + filepath.ToSlash(filepath.Join(base, "outside.xsd")), "must be a relative path"},
		{"dot-dot traversal to schema", "../outside.xsd", "must name a .xsd file inside"},
		{"deep dot-dot traversal", "../../../../../../../../etc/hostname.xsd", "must name a .xsd file inside"},
		{"dot-dot traversal to secret", "../secret.xsd", "must name a .xsd file inside"},
		{"symlink pointing outside root", "escape.xsd", "must name a .xsd file inside"},
		{"symlink to secret outside root", "secret-link.xsd", "must name a .xsd file inside"},
		{"symlink to non-xsd inside root", "notes.xsd", "must name a .xsd file inside"},
		{"non-xsd extension", "notes.txt", "must reference a .xsd file"},
		{"non-regular file", "dir.xsd", "is not a regular file"},
		{"oversized file", "big.xsd", "exceeds the"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			valid, err := XMLValidator{}.ValidateSchema(xmlWithSchemaLocation(tt.loc), filepath.Join(root, "doc.xml"))
			require.False(t, valid)
			require.ErrorContains(t, err, tt.wantErr)
			require.NotContains(t, err.Error(), schemaLocationSecret)
			require.NotContains(t, err.Error(), "schema compilation error")
		})
	}
}

func Test_XMLSchemaLocationAllowsContainedPaths(t *testing.T) {
	t.Parallel()
	_, root := schemaLocationFixture(t)
	require.NoError(t, os.Symlink(filepath.Join(root, "schemas", "s.xsd"), filepath.Join(root, "alias.xsd")))

	for _, loc := range []string{"schema.xsd", "./schema.xsd", "schemas/s.xsd", "schemas/../schema.xsd", "alias.xsd"} {
		t.Run(loc, func(t *testing.T) {
			t.Parallel()
			valid, err := XMLValidator{}.ValidateSchema(xmlWithSchemaLocation(loc), filepath.Join(root, "doc.xml"))
			require.NoError(t, err)
			require.True(t, valid)
		})
	}
}

func Test_XMLSchemaLocationWorkingDirectory(t *testing.T) {
	// Not parallel: changes the working directory.
	base, root := schemaLocationFixture(t)
	t.Chdir(root)

	sub := filepath.Join("nested", "doc.xml")
	valid, err := XMLValidator{}.ValidateSchema(xmlWithSchemaLocation("schemas/s.xsd"), sub)
	require.NoError(t, err)
	require.True(t, valid)

	valid, err = XMLValidator{}.ValidateSchema(xmlWithSchemaLocation("../schema.xsd"), sub)
	require.NoError(t, err)
	require.True(t, valid)

	valid, err = XMLValidator{}.ValidateSchema(xmlWithSchemaLocation("../outside.xsd"), "doc.xml")
	require.False(t, valid)
	require.ErrorContains(t, err, "must name a .xsd file inside")
	require.FileExists(t, filepath.Join(base, "outside.xsd"))
}
