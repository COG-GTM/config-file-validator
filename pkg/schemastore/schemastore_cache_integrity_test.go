package schemastore

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Boeing/config-file-validator/v2/pkg/tools"
)

func newTestStore(t *testing.T, schemaURL string) (*Store, string) {
	t.Helper()
	cacheDir := t.TempDir()
	return &Store{
		entries:  []catalogEntry{{FileMatch: []string{"config.json"}, URL: schemaURL}},
		cacheDir: cacheDir,
		cacheTTL: defaultCacheTTL,
	}, cacheDir
}

func requireEmptyCache(t *testing.T, cacheDir string) {
	t.Helper()
	var files []string
	require.NoError(t, filepath.WalkDir(cacheDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return err
	}))
	require.Empty(t, files)
}

func TestResolveOversizedSchemaLeavesNoCacheFile(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"description":"`))
		_, _ = w.Write([]byte(strings.Repeat("a", tools.MaxRemoteSchemaSize)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer srv.Close()

	schemaURL := srv.URL + "/big.json"
	store, cacheDir := newTestStore(t, schemaURL)

	_, found := store.Resolve("/project/config.json")
	require.True(t, found)

	cachePath, err := store.cachePathForURL(schemaURL)
	require.NoError(t, err)
	require.NoFileExists(t, cachePath)
	requireEmptyCache(t, cacheDir)
}

func TestResolveTruncatedDownloadLeavesNoCacheFile(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"object","properties":{`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer srv.Close()

	schemaURL := srv.URL + "/truncated.json"
	store, cacheDir := newTestStore(t, schemaURL)

	_, found := store.Resolve("/project/config.json")
	require.True(t, found)

	_, hit := store.lookupCache(schemaURL)
	require.False(t, hit)
	requireEmptyCache(t, cacheDir)
}

func TestResolveInvalidJSONSchemaNotCached(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>not a schema</html>`))
	}))
	defer srv.Close()

	schemaURL := srv.URL + "/schema.json"
	store, cacheDir := newTestStore(t, schemaURL)

	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, schemaURL, path)
	requireEmptyCache(t, cacheDir)
}

func TestLookupCacheRejectsCorruptOrOversizedFile(t *testing.T) {
	t.Parallel()
	tests := map[string][]byte{
		"truncated JSON": []byte(`{"type":"object","properties":{`),
		"oversized":      []byte(`"` + strings.Repeat("a", tools.MaxRemoteSchemaSize) + `"`),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			schemaURL := "https://example.com/schemas/config.json"
			store, _ := newTestStore(t, schemaURL)
			cachePath, err := store.cachePathForURL(schemaURL)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0755))
			require.NoError(t, os.WriteFile(cachePath, content, 0600))

			_, hit := store.lookupCache(schemaURL)
			require.False(t, hit)
		})
	}
}

func TestLookupCacheRejectsDirectory(t *testing.T) {
	t.Parallel()
	schemaURL := "https://example.com/schemas/config.json"
	store, _ := newTestStore(t, schemaURL)
	cachePath, err := store.cachePathForURL(schemaURL)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(cachePath, 0755))

	_, hit := store.lookupCache(schemaURL)
	require.False(t, hit)
}

func TestWriteFileAtomicRenameFailureCleansUp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "schema.json")
	// A non-empty directory at the target path makes the rename fail.
	require.NoError(t, os.MkdirAll(filepath.Join(target, "child"), 0755))

	require.Error(t, writeFileAtomic(target, []byte(`{}`)))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "schema.json", entries[0].Name())
}
