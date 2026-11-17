//go:build unix

package configfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func newRepo(t *testing.T) string {
	t.Helper()
	setHome(t, t.TempDir())
	repo := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".git"), 0o700))
	return repo
}

func TestDiscoverRejectsFileOwnedByAnotherUser(t *testing.T) {
	repo := newRepo(t)
	writeConfig(t, repo, `no-schema = true`)

	orig := currentUID
	t.Cleanup(func() { currentUID = orig })
	currentUID = func() int { return os.Geteuid() + 4242 }

	path, warnings := DiscoverWithWarnings(repo)
	require.Empty(t, path)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], filepath.Join(repo, FileName))
	require.Contains(t, warnings[0], "not the current user")
}

func TestDiscoverRejectsWorldWritableFile(t *testing.T) {
	repo := newRepo(t)
	writeConfig(t, repo, `no-schema = true`)
	require.NoError(t, os.Chmod(filepath.Join(repo, FileName), 0o666))

	path, warnings := DiscoverWithWarnings(repo)
	require.Empty(t, path)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "file is writable by all users")
}

func TestDiscoverRejectsWorldWritableDirWithoutSticky(t *testing.T) {
	repo := newRepo(t)
	writeConfig(t, repo, `no-schema = true`)
	require.NoError(t, os.Chmod(repo, 0o777))
	t.Cleanup(func() { _ = os.Chmod(repo, 0o700) })

	path, warnings := DiscoverWithWarnings(repo)
	require.Empty(t, path)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "directory is writable by all users")
}

func TestDiscoverAllowsStickyWorldWritableDir(t *testing.T) {
	repo := newRepo(t)
	writeConfig(t, repo, `quiet = true`)
	require.NoError(t, os.Chmod(repo, 0o777|os.ModeSticky))
	t.Cleanup(func() { _ = os.Chmod(repo, 0o700) })

	path, warnings := DiscoverWithWarnings(repo)
	require.Equal(t, filepath.Join(repo, FileName), path)
	require.Empty(t, warnings)
}

func TestUntrustedReasonDirStatError(t *testing.T) {
	repo := newRepo(t)
	writeConfig(t, repo, `quiet = true`)
	info, err := os.Stat(filepath.Join(repo, FileName))
	require.NoError(t, err)

	reason := untrustedReason(filepath.Join(repo, FileName), info, filepath.Join(repo, "missing"))
	require.Contains(t, reason, "cannot inspect directory")
}
