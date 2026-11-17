package reporter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_outputBytesToFile(t *testing.T) {
	content := []byte("this is an example file.\nthis is for outputBytesToFile function.\n")

	t.Run("existing dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := outputBytesToFile(tmpDir, "default", "txt", content)
		require.NoError(t, err)

		actual, err := os.ReadFile(tmpDir + "/default.txt")
		require.NoError(t, err)
		assert.Equal(t, content, actual)
	})

	t.Run("file name provided", func(t *testing.T) {
		tmpDir := t.TempDir()
		outPath := tmpDir + "/validator_result.json"
		err := outputBytesToFile(outPath, "default", "json", content)
		require.NoError(t, err)

		actual, err := os.ReadFile(outPath)
		require.NoError(t, err)
		assert.Equal(t, content, actual)
	})

	t.Run("existing dir without extension", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := outputBytesToFile(tmpDir, "default", "", content)
		require.NoError(t, err)

		actual, err := os.ReadFile(tmpDir + "/default")
		require.NoError(t, err)
		assert.Equal(t, content, actual)
	})

	t.Run("empty string outputDest", func(t *testing.T) {
		err := outputBytesToFile("", "default", ".txt", content)
		require.Error(t, err)
		assert.Regexp(t, "outputDest is an empty string", err.Error())
	})

	t.Run("non-existing dir", func(t *testing.T) {
		err := outputBytesToFile("/nonexistent/path/output", "result", "", content)
		require.Error(t, err)
		assert.Regexp(t, "failed to create a file", err.Error())
	})
}

func Test_outputBytesToFileRefusesSymlink(t *testing.T) {
	content := []byte("report content\n")
	original := []byte("do not overwrite\n")

	tests := []struct {
		name       string
		outputDest func(dir string) string
		linkName   string
	}{
		{
			name:       "symlink at output file path",
			outputDest: func(dir string) string { return filepath.Join(dir, "results.xml") },
			linkName:   "results.xml",
		},
		{
			name:       "symlink at default name in output dir",
			outputDest: func(dir string) string { return dir },
			linkName:   "result.xml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outDir := t.TempDir()
			target := filepath.Join(t.TempDir(), "victim")
			require.NoError(t, os.WriteFile(target, original, 0o600))
			if err := os.Symlink(target, filepath.Join(outDir, tt.linkName)); err != nil {
				t.Skipf("symlinks not supported: %v", err)
			}

			err := outputBytesToFile(tt.outputDest(outDir), "result", "xml", content)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "symbolic link")

			actual, err := os.ReadFile(target)
			require.NoError(t, err)
			assert.Equal(t, original, actual)
		})
	}
}

func Test_outputBytesToFileStatError(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	assert.NotPanics(t, func() {
		err := outputBytesToFile(filepath.Join(file, "child"), "result", "xml", []byte("x"))
		require.Error(t, err)
	})
}
