package reporter

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const validMergeSARIF = `{
  "version": "2.1.0",
  "$schema": "https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json",
  "runs": [{"tool": {"driver": {"name": "merged-tool"}}, "results": []}]
}`

func mergeSARIFWithin(t *testing.T, config SARIFMergeConfig) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := createSARIFReport([]Report{validReport}, config)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("SARIF merge did not return within 10s")
		return nil
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func Test_sarifMergeDirRejectsSymlinkToDevZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/dev/zero is not available on Windows")
	}
	dir := t.TempDir()
	symlinkOrSkip(t, "/dev/zero", filepath.Join(dir, "x.sarif"))

	err := mergeSARIFWithin(t, SARIFMergeConfig{Directory: dir})
	require.ErrorContains(t, err, "is not a regular file")
}

func Test_sarifMergeDirRejectsSymlinkToRegularFile(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.sarif")
	require.NoError(t, os.WriteFile(outside, []byte(validMergeSARIF), 0o600))
	dir := t.TempDir()
	symlinkOrSkip(t, outside, filepath.Join(dir, "linked.sarif"))

	err := mergeSARIFWithin(t, SARIFMergeConfig{Directory: dir})
	require.ErrorContains(t, err, "is not a regular file")
}

func Test_sarifMergeDirIgnoresNonSARIFSymlinks(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ok.sarif"), []byte(validMergeSARIF), 0o600))
	symlinkOrSkip(t, filepath.Join(dir, "ok.sarif"), filepath.Join(dir, "notes.txt"))

	log, err := createSARIFReport([]Report{validReport}, SARIFMergeConfig{Directory: dir})
	require.NoError(t, err)
	require.Len(t, log.Runs, 2)
}

func Test_sarifMergeFileRejectsDevZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/dev/zero is not available on Windows")
	}
	link := filepath.Join(t.TempDir(), "x.sarif")
	symlinkOrSkip(t, "/dev/zero", link)

	for _, path := range []string{"/dev/zero", link} {
		err := mergeSARIFWithin(t, SARIFMergeConfig{Files: []string{path}})
		require.ErrorContains(t, err, "not a regular file")
	}
}

func Test_sarifMergeRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.sarif")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(maxSARIFMergeFileSize+1))
	require.NoError(t, f.Close())

	err = mergeSARIFWithin(t, SARIFMergeConfig{Files: []string{path}})
	require.ErrorContains(t, err, "exceeds the maximum size")

	err = mergeSARIFWithin(t, SARIFMergeConfig{Directory: dir})
	require.ErrorContains(t, err, "exceeds the maximum size")
}

func Test_sarifMergeDirRejectsTooManyFiles(t *testing.T) {
	dir := t.TempDir()
	for i := range maxSARIFMergeFiles + 1 {
		require.NoError(t, os.WriteFile(filepath.Join(dir, strconv.Itoa(i)+".sarif"), nil, 0o600))
	}

	_, err := sarifFilesInDirectory(dir)
	require.ErrorContains(t, err, "more than 1000 SARIF files")
}

func Test_sarifMergeDirDepthLimit(t *testing.T) {
	root := t.TempDir()
	deepest := root
	for range maxSARIFMergeDirDepth {
		deepest = filepath.Join(deepest, "d")
	}
	require.NoError(t, os.MkdirAll(deepest, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deepest, "ok.sarif"), nil, 0o600))

	paths, err := sarifFilesInDirectory(root)
	require.NoError(t, err)
	require.Len(t, paths, 1)

	require.NoError(t, os.Mkdir(filepath.Join(deepest, "d"), 0o700))
	_, err = sarifFilesInDirectory(root)
	require.ErrorContains(t, err, "exceeds the maximum depth of 32")
}

func Test_sarifMergeRejectsMalformedRuns(t *testing.T) {
	cases := map[string]string{
		"run is not an object":    `[1]`,
		"run is null":             `null`,
		"missing tool":            `{"results": []}`,
		"empty driver name":       `{"tool": {"driver": {"name": ""}}}`,
		"driver name not string":  `{"tool": {"driver": {"name": 7}}}`,
		"results not an array":    `{"tool": {"driver": {"name": "t"}}, "results": {}}`,
		"result is not an object": `{"tool": {"driver": {"name": "t"}}, "results": ["x"]}`,
		"result is null":          `{"tool": {"driver": {"name": "t"}}, "results": [null]}`,
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.sarif")
			doc := `{"version": "2.1.0", "runs": [` + run + `]}`
			require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))

			_, err := createSARIFReport([]Report{validReport}, SARIFMergeConfig{Files: []string{path}})
			require.ErrorContains(t, err, "run 0")
		})
	}
}
