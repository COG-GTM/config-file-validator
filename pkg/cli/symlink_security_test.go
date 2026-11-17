package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Boeing/config-file-validator/v2/internal/testhelper"
	"github.com/Boeing/config-file-validator/v2/pkg/finder"
	"github.com/Boeing/config-file-validator/v2/pkg/reporter"
)

const outsideSecret = "machine registry.example.com login build password s3cr3t-token"

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
}

func reportText(r reporter.Report) string {
	text := strings.Join(r.ValidationErrors, "\n")
	if r.ValidationError != nil {
		text += "\n" + r.ValidationError.Error()
	}
	return text
}

func Test_CLISymlinkOutsideSearchPathNotRead(t *testing.T) {
	outside := t.TempDir()
	secret := testhelper.WriteFile(t, outside, "netrc", outsideSecret+"\n")

	root := t.TempDir()
	testhelper.WriteFile(t, root, "good.json", `{"a": 1}`)
	symlinkOrSkip(t, secret, filepath.Join(root, "leak.ini"))
	symlinkOrSkip(t, filepath.Join(outside, "missing"), filepath.Join(root, "probe.ini"))

	rep := &captureReporter{}
	c := Init(
		WithFinder(finder.FileSystemFinderInit(finder.WithPathRoots(root))),
		WithReporters(rep),
	)
	exitStatus, err := c.Run()
	require.NoError(t, err)
	require.Equal(t, 1, exitStatus)

	byName := map[string]reporter.Report{}
	for _, r := range rep.reports {
		require.NotContains(t, reportText(r), "s3cr3t-token")
		byName[r.FileName] = r
	}
	require.True(t, byName["good.json"].IsValid)
	require.False(t, byName["leak.ini"].IsValid)
	require.False(t, byName["probe.ini"].IsValid)
	require.Equal(t, "other", byName["leak.ini"].ErrorType)
	// Existing and missing targets outside the search path must look the same.
	require.Equal(t, reportText(byName["leak.ini"]), reportText(byName["probe.ini"]))
}

func Test_CLISymlinkToDevZeroEndsQuickly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/dev/zero not available")
	}
	root := t.TempDir()
	symlinkOrSkip(t, "/dev/zero", filepath.Join(root, "zero.json"))

	rep := &captureReporter{}
	c := Init(
		WithFinder(finder.FileSystemFinderInit(finder.WithPathRoots(root))),
		WithReporters(rep),
	)

	type result struct {
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		status, err := c.Run()
		done <- result{status, err}
	}()

	select {
	case res := <-done:
		require.NoError(t, res.err)
		require.Equal(t, 1, res.status)
		require.Len(t, rep.reports, 1)
		require.False(t, rep.reports[0].IsValid)
	case <-time.After(10 * time.Second):
		t.Fatal("validator kept reading from a symlink to /dev/zero")
	}
}

func Test_CLIFileLargerThanLimitNotRead(t *testing.T) {
	root := t.TempDir()
	testhelper.WriteFile(t, root, "big.json", `{"key": "`+strings.Repeat("a", 2048)+`"}`)

	rep := &captureReporter{}
	c := Init(
		WithFinder(finder.FileSystemFinderInit(finder.WithPathRoots(root))),
		WithReporters(rep),
	)
	c.maxFileSize = 1024
	exitStatus, err := c.Run()
	require.NoError(t, err)
	require.Equal(t, 1, exitStatus)
	require.Len(t, rep.reports, 1)
	require.False(t, rep.reports[0].IsValid)
	require.ErrorIs(t, rep.reports[0].ValidationError, errFileTooLarge)
}

func Test_readFileBoundedRejectsNonRegular(t *testing.T) {
	_, err := readFileBounded(t.TempDir(), DefaultMaxFileSize)
	require.ErrorIs(t, err, errNotRegularFile)
}
