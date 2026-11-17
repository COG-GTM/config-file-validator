package schemastore

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveRejectsCachePathTraversal(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"object"}`))
	}))
	t.Cleanup(srv.Close)

	tests := []struct {
		name string
		path string
	}{
		{name: "encoded dot-dot slash", path: "/..%2f..%2f..%2fescape.json"},
		{name: "encoded dots", path: "/%2e%2e/%2e%2e/%2e%2e/escape.json"},
		{name: "plain dot-dot", path: "/../../../escape.json"},
		{name: "encoded backslash", path: "/..%5c..%5c..%5cescape.json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			cacheDir := filepath.Join(root, "a", "b", "cache")
			require.NoError(t, os.MkdirAll(cacheDir, 0o755))

			schemaURL := srv.URL + tt.path
			store := &Store{
				entries: []catalogEntry{
					{FileMatch: []string{"config.json"}, URL: schemaURL},
				},
				cacheDir: cacheDir,
				cacheTTL: defaultCacheTTL,
			}

			path, found := store.Resolve("/project/config.json")
			require.True(t, found)
			require.Equal(t, schemaURL, path, "traversal URL must not resolve to a cache file")

			err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				rel, relErr := filepath.Rel(cacheDir, p)
				require.NoError(t, relErr)
				require.True(t, filepath.IsLocal(rel), "file written outside cache dir: %s", p)
				return nil
			})
			require.NoError(t, err)
		})
	}
}

func TestLookupCacheIgnoresFileOutsideCacheDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cacheDir := filepath.Join(root, "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	outside := filepath.Join(root, "secret.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"type":"object"}`), 0o600))

	schemaURL := "http://127.0.0.1:1/../../secret.json" // DevSkim: ignore DS162092 - test address, not debug code
	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: schemaURL},
		},
		cacheDir: cacheDir,
		cacheTTL: defaultCacheTTL,
	}

	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, schemaURL, path)
}

func TestCachePathForURLStaysInCacheDir(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	store := &Store{cacheDir: cacheDir}

	got, err := store.cachePathForURL("https://www.schemastore.org/package.json")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cacheDir, "www.schemastore.org", "package.json"), got)

	for _, u := range []string{
		"https://example.com/../escape.json",
		"https://example.com/..%2fescape.json",
		"https://example.com/",
		"https://example.com",
		"https://../escape.json",
		"file:///etc/passwd/..",
	} {
		_, err := store.cachePathForURL(u)
		require.Error(t, err, u)
	}
}

func TestFetchAndCacheTruncatedBodyLeavesNoCacheFile(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":`))
	}))
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()
	schemaURL := srv.URL + "/schema.json"
	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: schemaURL},
		},
		cacheDir: cacheDir,
		cacheTTL: defaultCacheTTL,
	}

	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, schemaURL, path)

	entries, err := os.ReadDir(filepath.Join(cacheDir, srv.Listener.Addr().String()))
	require.NoError(t, err)
	require.Empty(t, entries, "partial download must not leave files in the cache")
}

func TestFetchAndCacheRenameFailureFallsBack(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"object"}`))
	}))
	t.Cleanup(srv.Close)

	cacheDir := t.TempDir()
	schemaURL := srv.URL + "/schema.json"
	blocker := filepath.Join(cacheDir, srv.Listener.Addr().String(), "schema.json")
	require.NoError(t, os.MkdirAll(filepath.Join(blocker, "child"), 0o755))

	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: schemaURL},
		},
		cacheDir: cacheDir,
		cacheTTL: 0,
	}

	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, schemaURL, path)
}
