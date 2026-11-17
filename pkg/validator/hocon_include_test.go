package validator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Regression tests for HOCON include directives and environment fallback
// (local file inclusion via untrusted .hocon files).

func Test_HoconRejectsIncludes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.conf"), []byte("leaked = true\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "self.hocon"), []byte("include \"self.hocon\"\n"), 0o600))
	t.Chdir(dir)

	payloads := map[string]string{
		"bare":               `include "secret.conf"`,
		"file":               `include file("secret.conf")`,
		"classpath":          `include classpath("secret.conf")`,
		"required":           `include required(file("secret.conf"))`,
		"absolute":           `include required(file("/etc/hostname"))`,
		"traversal":          `include required(file("../../../../../../etc/hostname"))`,
		"self include":       `include "self.hocon"`,
		"tab separated":      "include\t\"secret.conf\"",
		"after newline":      "a = 1\ninclude \"secret.conf\"",
		"after comma":        "a = 1, include \"secret.conf\"",
		"nested object":      "a { include \"secret.conf\" }",
		"object in array":    "a = [ { include \"secret.conf\" } ]",
		"after char quote":   "a = 'x', include \"secret.conf\"",
		"after multi-line":   "a = \"\"\"x\"\"\"\ninclude \"secret.conf\"",
		"after substitution": "b = 1\na = ${b}\ninclude \"secret.conf\"",
	}

	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			valid, err := HoconValidator{}.ValidateSyntax([]byte(payload))
			require.False(t, valid)
			require.ErrorIs(t, err, ErrHoconInclude)

			var ve *ValidationError
			require.ErrorAs(t, err, &ve)
			require.Positive(t, ve.Line)
		})
	}
}

func Test_HoconIncludeLookalikesStillValid(t *testing.T) {
	t.Parallel()

	inputs := map[string]string{
		"hash comment":      "# include \"secret.conf\"\na = 1",
		"slash comment":     "// include \"secret.conf\"\na = 1",
		"quoted string":     `a = "include \"secret.conf\""`,
		"multi-line string": "a = \"\"\"\ninclude \"secret.conf\"\n\"\"\"",
		"unquoted value":    "a = include",
		"key prefix":        "includes = [1, 2]",
		"substitution":      "include-dir = x\na = ${include-dir}",
	}

	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			valid, err := HoconValidator{}.ValidateSyntax([]byte(input))
			require.NoError(t, err)
			require.True(t, valid)
		})
	}
}

func Test_HoconSubstitutionsIgnoreEnvironment(t *testing.T) {
	const name = "CFV_HOCON_TEST_VAR"

	cases := []struct {
		name  string
		input string
		valid bool
	}{
		{"required env reference", "a = ${" + name + "}", false},
		{"optional env reference", "a = ${?" + name + "}", true},
		{"optional env reference concatenated with object", "a = {} ${?" + name + "}", true},
		{"optional env override", "a = 1\na = ${?" + name + "}", true},
		{"internal reference", "b = 2\na = ${b}", true},
		{"internal reference with spaces", "b = 2\na = ${ b }", true},
		{"object merge", "b = {x = 1}\na = ${b} {y = 2}", true},
		{"cycle", "a = ${b}\nb = ${a}", false},
		{"array root", "[${" + name + "}]", true},
	}

	for _, set := range []bool{false, true} {
		if set {
			t.Setenv(name, "value")
		} else {
			require.NoError(t, os.Unsetenv(name))
		}
		for _, tc := range cases {
			valid, err := HoconValidator{}.ValidateSyntax([]byte(tc.input))
			require.Equal(t, tc.valid, valid, "%s (env set: %v): %v", tc.name, set, err)
			if !tc.valid {
				require.Error(t, err)
			}
		}
	}
}

func Test_HoconUndefinedSubstitutionPosition(t *testing.T) {
	t.Parallel()

	valid, err := HoconValidator{}.ValidateSyntax([]byte("a = 1\nb = ${missing.path}"))
	require.False(t, valid)

	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, 2, ve.Line)
	require.Equal(t, 5, ve.Column)
	require.Contains(t, ve.Error(), "${missing.path}")
}
