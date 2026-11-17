package schemastore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func setupTestBundle(t *testing.T, entries []catalogEntry) string {
	t.Helper()
	dir := t.TempDir()

	catalogDir := filepath.Join(dir, "src", "api", "json")
	require.NoError(t, os.MkdirAll(catalogDir, 0755))

	schemaDir := filepath.Join(dir, "src", "schemas", "json")
	require.NoError(t, os.MkdirAll(schemaDir, 0755))

	cat := catalog{Schemas: entries}
	data, err := json.Marshal(cat)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(catalogDir, "catalog.json"), data, 0600))

	return dir
}

func writeSchema(t *testing.T, bundlePath, filename, content string) string {
	t.Helper()
	p := filepath.Join(bundlePath, "src", "schemas", "json", filename)
	require.NoError(t, os.WriteFile(p, []byte(content), 0600))
	return p
}

func TestOpen(t *testing.T) {
	t.Parallel()
	entries := []catalogEntry{
		{FileMatch: []string{"package.json"}, URL: "https://www.schemastore.org/package.json"},
		{FileMatch: []string{"*.hcl"}, URL: "https://www.schemastore.org/hcl.json"},         // unsupported ext, not extensionless
		{FileMatch: []string{"tsconfig*.json"}, URL: "https://www.schemastore.org/ts.json"}, // supported
	}
	dir := setupTestBundle(t, entries)
	store, err := Open(dir)
	require.NoError(t, err)
	// hcl entry should be filtered out
	require.Len(t, store.entries, 2)
}

func TestOpenMissingCatalog(t *testing.T) {
	t.Parallel()
	_, err := Open(t.TempDir())
	require.Error(t, err)
}

func TestLookupExactMatch(t *testing.T) {
	t.Parallel()
	entries := []catalogEntry{
		{FileMatch: []string{"package.json"}, URL: "https://www.schemastore.org/package.json"},
	}
	dir := setupTestBundle(t, entries)
	writeSchema(t, dir, "package.json", `{"type":"object"}`)

	store, err := Open(dir)
	require.NoError(t, err)

	path, found := store.Resolve("/some/project/package.json")
	require.True(t, found)
	require.Contains(t, path, "package.json")
}

func TestLookupGlobMatch(t *testing.T) {
	t.Parallel()
	entries := []catalogEntry{
		{FileMatch: []string{"**/.github/workflows/*.yml"}, URL: "https://www.schemastore.org/github-workflow.json"},
	}
	dir := setupTestBundle(t, entries)
	writeSchema(t, dir, "github-workflow.json", `{"type":"object"}`)

	store, err := Open(dir)
	require.NoError(t, err)

	path, found := store.Resolve("/repo/.github/workflows/ci.yml")
	require.True(t, found)
	require.Contains(t, path, "github-workflow.json")
}

func TestLookupNoMatch(t *testing.T) {
	t.Parallel()
	entries := []catalogEntry{
		{FileMatch: []string{"package.json"}, URL: "https://www.schemastore.org/package.json"},
	}
	dir := setupTestBundle(t, entries)
	writeSchema(t, dir, "package.json", `{"type":"object"}`)

	store, err := Open(dir)
	require.NoError(t, err)

	_, found := store.Resolve("/some/project/tsconfig.json")
	require.False(t, found)
}

func TestLookupExternalURLSkipped(t *testing.T) {
	t.Parallel()
	entries := []catalogEntry{
		{FileMatch: []string{"foo.json"}, URL: "https://example.com/foo.json"},
	}
	dir := setupTestBundle(t, entries)

	store, err := Open(dir)
	require.NoError(t, err)

	// External URLs are now returned as remote fallback
	path, found := store.Resolve("/project/foo.json")
	require.True(t, found)
	require.Equal(t, "https://example.com/foo.json", path)
}

func TestLookupMissingSchemaFile(t *testing.T) {
	t.Parallel()
	entries := []catalogEntry{
		{FileMatch: []string{"package.json"}, URL: "https://www.schemastore.org/package.json"},
	}
	dir := setupTestBundle(t, entries)
	// Don't write the schema file — should fall back to remote URL or cache

	store, err := Open(dir)
	require.NoError(t, err)

	path, found := store.Resolve("/project/package.json")
	require.True(t, found)
	// Result is either a cached local path or the remote URL
	require.Contains(t, path, "package.json")
}

func TestLookupGlobStarJson(t *testing.T) {
	t.Parallel()
	entries := []catalogEntry{
		{FileMatch: []string{"tsconfig*.json"}, URL: "https://www.schemastore.org/tsconfig.json"},
	}
	dir := setupTestBundle(t, entries)
	writeSchema(t, dir, "tsconfig.json", `{"type":"object"}`)

	store, err := Open(dir)
	require.NoError(t, err)

	path, found := store.Resolve("tsconfig.build.json")
	require.True(t, found)
	require.Contains(t, path, "tsconfig.json")
}

func TestOpenEmbedded(t *testing.T) {
	t.Parallel()
	store, err := OpenEmbedded()
	require.NoError(t, err)
	require.NotEmpty(t, store.entries)
	require.Empty(t, store.schemaDir)
}

func TestOpenEmbeddedLookupReturnsRemoteURL(t *testing.T) {
	t.Parallel()
	store, err := OpenEmbedded()
	require.NoError(t, err)

	// package.json is in the SchemaStore catalog
	path, found := store.Resolve("/project/package.json")
	require.True(t, found)
	// Result is either a cached local path or the remote URL
	require.Contains(t, path, "package.json")
}

func TestLookupLocalPriorityOverRemote(t *testing.T) {
	t.Parallel()
	entries := []catalogEntry{
		{FileMatch: []string{"package.json"}, URL: "https://www.schemastore.org/package.json"},
	}
	dir := setupTestBundle(t, entries)
	localPath := writeSchema(t, dir, "package.json", `{"type":"object"}`)

	store, err := Open(dir)
	require.NoError(t, err)

	path, found := store.Resolve("/project/package.json")
	require.True(t, found)
	require.Equal(t, localPath, path)
}

func TestLookupCacheHit(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()

	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: "https://example.com/schemas/config.json"},
		},
		cacheDir: cacheDir,
		cacheTTL: defaultCacheTTL,
	}

	// Pre-populate cache
	cachedPath, err := store.cachePathForURL("https://example.com/schemas/config.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cachedPath, []byte(`{"type":"object"}`), 0600))

	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, cachedPath, path)
}

func TestLookupCacheExpired(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()

	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: "https://example.com/nonexistent.json"},
		},
		cacheDir: cacheDir,
		cacheTTL: 0, // expired immediately
	}

	// Pre-populate cache with old mtime
	cachedPath, err := store.cachePathForURL("https://example.com/nonexistent.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cachedPath, []byte(`{"type":"object"}`), 0600))
	oldTime := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(cachedPath, oldTime, oldTime))

	// Cache is expired, fetch will fail (fake URL), falls back to remote URL
	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, "https://example.com/nonexistent.json", path)
}

func TestLookupNoCacheDir(t *testing.T) {
	t.Parallel()

	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: "https://example.com/nonexistent.json"},
		},
		cacheDir: "",
		cacheTTL: defaultCacheTTL,
	}

	// No cache dir, fetch will fail, falls back to remote URL
	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, "https://example.com/nonexistent.json", path)
}

func TestFetchAndCacheSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"object"}`))
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: srv.URL + "/schema.json"},
		},
		cacheDir: cacheDir,
		cacheTTL: defaultCacheTTL,
	}

	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, cacheDir, filepath.Dir(path))
	require.Contains(t, filepath.Base(path), "schema.json")

	// Verify file was cached
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"object"}`, string(data))
}

func TestFetchAndCache404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: srv.URL + "/missing.json"},
		},
		cacheDir: cacheDir,
		cacheTTL: defaultCacheTTL,
	}

	// Fetch fails, falls back to remote URL
	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, srv.URL+"/missing.json", path)
}

func TestFetchAndCache500(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: srv.URL + "/error.json"},
		},
		cacheDir: cacheDir,
		cacheTTL: defaultCacheTTL,
	}

	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, srv.URL+"/error.json", path)
}

func TestFetchAndCacheNetworkError(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: "http://127.0.0.1:1/unreachable.json"}, // DevSkim: ignore DS162092 - test server address, not debug code
		},
		cacheDir: cacheDir,
		cacheTTL: defaultCacheTTL,
	}

	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, "http://127.0.0.1:1/unreachable.json", path) // DevSkim: ignore DS162092 - test server address, not debug code
}

func TestFetchAndCacheThenHitCache(t *testing.T) {
	t.Parallel()
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"object"}`))
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: srv.URL + "/schema.json"},
		},
		cacheDir: cacheDir,
		cacheTTL: defaultCacheTTL,
	}

	// First call fetches
	path1, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, 1, callCount)

	// Second call hits cache
	path2, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, 1, callCount) // no additional fetch
	require.Equal(t, path1, path2)
}

func TestFetchAndCacheUnwritableDir(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"object"}`))
	}))
	defer srv.Close()

	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: srv.URL + "/schema.json"},
		},
		cacheDir: "/nonexistent/readonly/path",
		cacheTTL: defaultCacheTTL,
	}

	// Cache write fails, falls back to remote URL
	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, srv.URL+"/schema.json", path)
}

func TestCachePathForURLStaysInCacheDir(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	store := &Store{cacheDir: cacheDir, cacheTTL: defaultCacheTTL}

	urls := []string{
		"https://www.schemastore.org/package.json",
		"http://evil.example/../../../../.bashrc",
		"http://evil.example/..%2f..%2f..%2f.bashrc",
		"http://evil.example/%2e%2e/%2e%2e/escape.json",
		"http://evil.example/schema.json%2f..%2f..%2fx",
		"http://../../escape.json",
		"file:///etc/passwd",
	}
	seen := map[string]string{}
	for _, u := range urls {
		p, err := store.cachePathForURL(u)
		require.NoError(t, err, u)
		require.Equal(t, cacheDir, filepath.Dir(p), u)
		require.True(t, filepath.IsLocal(filepath.Base(p)), u)
		prev, dup := seen[p]
		require.False(t, dup, "%s and %s map to the same cache file", u, prev)
		seen[p] = u
	}
}

func TestFetchAndCacheTraversalURLsStayInCacheDir(t *testing.T) {
	t.Parallel()
	const payload = `{"pwned":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	root := t.TempDir()
	cacheDir := filepath.Join(root, "a", "b", "c", "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))

	urls := []string{
		srv.URL + "/../../../../escape-plain.json",
		srv.URL + "/..%2f..%2f..%2f..%2fescape-encoded.json",
		srv.URL + "/%2e%2e/%2e%2e/%2e%2e/%2e%2e/escape-dots.json",
	}
	for _, u := range urls {
		store := &Store{
			entries:  []catalogEntry{{FileMatch: []string{"config.json"}, URL: u}},
			cacheDir: cacheDir,
			cacheTTL: defaultCacheTTL,
		}
		p, found := store.Resolve("/project/config.json")
		require.True(t, found, u)
		require.Equal(t, cacheDir, filepath.Dir(p), u)
	}

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		require.Equal(t, cacheDir, filepath.Dir(p), "file written outside cache dir: %s", p)
		require.NotContains(t, filepath.Base(p), ".tmp", "temp file left behind: %s", p)
		return nil
	})
	require.NoError(t, err)

	entries, err := os.ReadDir(cacheDir)
	require.NoError(t, err)
	require.Len(t, entries, len(urls))
}

func TestFetchAndCacheTruncatedBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":`))
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: srv.URL + "/schema.json"},
		},
		cacheDir: cacheDir,
		cacheTTL: defaultCacheTTL,
	}

	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Equal(t, srv.URL+"/schema.json", path)

	entries, err := os.ReadDir(cacheDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestOpenKeepsExtensionlessFileMatch(t *testing.T) {
	t.Parallel()
	entries := []catalogEntry{
		{FileMatch: []string{".babelrc"}, URL: "https://www.schemastore.org/babelrc.json"},
		{FileMatch: []string{"Pipfile"}, URL: "https://example.com/pipfile.json"},
		{FileMatch: []string{".clangd"}, URL: "https://www.schemastore.org/clangd.json"},
		{FileMatch: []string{"*.css"}, URL: "https://example.com/css.json"}, // unsupported, not extensionless
	}
	dir := setupTestBundle(t, entries)
	store, err := Open(dir)
	require.NoError(t, err)
	// .babelrc, Pipfile, .clangd should be kept; *.css should be filtered
	require.Len(t, store.entries, 3)
}

func TestResolveExtensionlessFile(t *testing.T) {
	t.Parallel()
	entries := []catalogEntry{
		{FileMatch: []string{".babelrc"}, URL: "https://www.schemastore.org/babelrc.json"},
	}
	dir := setupTestBundle(t, entries)
	writeSchema(t, dir, "babelrc.json", `{"type":"object"}`)

	store, err := Open(dir)
	require.NoError(t, err)

	path, found := store.Resolve("/project/.babelrc")
	require.True(t, found)
	require.Contains(t, path, "babelrc.json")
}

func TestResolveExtensionlessDotfileNoMatch(t *testing.T) {
	t.Parallel()
	entries := []catalogEntry{
		{FileMatch: []string{".babelrc"}, URL: "https://www.schemastore.org/babelrc.json"},
	}
	dir := setupTestBundle(t, entries)

	store, err := Open(dir)
	require.NoError(t, err)

	// Different dotfile should not match
	_, found := store.Resolve("/project/.eslintrc")
	require.False(t, found)
}
