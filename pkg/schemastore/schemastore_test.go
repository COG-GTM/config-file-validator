package schemastore

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubClient returns a client that answers every request with status and body
// without touching the network.
func stubClient(status int, body string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{},
			Request:    r,
		}, nil
	})}
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
	store.cacheDir = t.TempDir()
	store.client = stubClient(http.StatusOK, `{"type":"object"}`)

	// External URLs are fetched into the cache; the remote URL is never returned
	path, found := store.Resolve("/project/foo.json")
	require.True(t, found)
	require.Equal(t, filepath.Join(store.cacheDir, "example.com", "foo.json"), path)
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
	store.cacheDir = t.TempDir()
	store.client = stubClient(http.StatusOK, `{"type":"object"}`)

	path, found := store.Resolve("/project/package.json")
	require.True(t, found)
	require.Equal(t, filepath.Join(store.cacheDir, "www.schemastore.org", "package.json"), path)
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

func TestOpenEmbeddedLookupFetchesAndCaches(t *testing.T) {
	t.Parallel()
	store, err := OpenEmbedded()
	require.NoError(t, err)
	store.cacheDir = t.TempDir()
	store.client = stubClient(http.StatusOK, `{"type":"object"}`)

	// package.json is in the SchemaStore catalog
	path, found := store.Resolve("/project/package.json")
	require.True(t, found)
	require.True(t, strings.HasPrefix(path, store.cacheDir), path)
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
	cachedPath := filepath.Join(cacheDir, "example.com", "schemas", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(cachedPath), 0755))
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
		client:   stubClient(http.StatusNotFound, ""),
	}

	// Pre-populate cache with old mtime
	cachedPath := filepath.Join(cacheDir, "example.com", "nonexistent.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(cachedPath), 0755))
	require.NoError(t, os.WriteFile(cachedPath, []byte(`{"type":"object"}`), 0600))
	oldTime := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(cachedPath, oldTime, oldTime))

	// Cache is expired and the fetch fails: report the error instead of
	// handing the remote URL to the schema loader
	path, found, err := store.ResolveSchema("/project/config.json")
	require.True(t, found)
	require.Empty(t, path)
	require.ErrorContains(t, err, "HTTP 404")

	path, found = store.Resolve("/project/config.json")
	require.False(t, found)
	require.Empty(t, path)
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

	// No cache dir: nothing to fetch into, report the error
	path, found, err := store.ResolveSchema("/project/config.json")
	require.True(t, found)
	require.Empty(t, path)
	require.ErrorContains(t, err, "no cache directory")
}

func TestFetchAndCacheSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
		client:   srv.Client(),
	}

	path, found := store.Resolve("/project/config.json")
	require.True(t, found)
	require.Contains(t, path, "schema.json")

	// Verify file was cached
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"object"}`, string(data))
}

func TestFetchAndCache404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
		client:   srv.Client(),
	}

	path, found, err := store.ResolveSchema("/project/config.json")
	require.True(t, found)
	require.Empty(t, path)
	require.ErrorContains(t, err, "HTTP 404")
}

func TestFetchAndCache500(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
		client:   srv.Client(),
	}

	path, found, err := store.ResolveSchema("/project/config.json")
	require.True(t, found)
	require.Empty(t, path)
	require.ErrorContains(t, err, "HTTP 500")
}

func TestFetchAndCacheNetworkError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	client := srv.Client()
	unreachable := srv.URL + "/unreachable.json"
	srv.Close()

	store := &Store{
		entries: []catalogEntry{
			{FileMatch: []string{"config.json"}, URL: unreachable},
		},
		cacheDir: t.TempDir(),
		cacheTTL: defaultCacheTTL,
		client:   client,
	}

	path, found, err := store.ResolveSchema("/project/config.json")
	require.True(t, found)
	require.Empty(t, path)
	require.ErrorContains(t, err, "fetching schema")
	require.NotErrorIs(t, err, ErrBlockedURL)
}

func TestFetchAndCacheThenHitCache(t *testing.T) {
	t.Parallel()
	callCount := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
		client:   srv.Client(),
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
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
		client:   srv.Client(),
	}

	// Cache write fails: report the error, never return the remote URL
	path, found, err := store.ResolveSchema("/project/config.json")
	require.True(t, found)
	require.Empty(t, path)
	require.ErrorContains(t, err, "creating cache directory")
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

// countingServer returns a TLS test server that counts requests and serves a schema.
func countingServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if handler != nil {
			handler(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"type":"object"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// allowOnly returns an address check that permits only srv's listener and
// applies the default public-address policy to everything else.
func allowOnly(t *testing.T, srv *httptest.Server) func(netip.AddrPort) error {
	t.Helper()
	allowed := netip.MustParseAddrPort(srv.Listener.Addr().String())
	return func(ap netip.AddrPort) error {
		if ap == allowed {
			return nil
		}
		return checkPublicAddr(ap)
	}
}

// policyClient builds the production client with a custom address check and
// trusts the httptest certificate.
func policyClient(t *testing.T, srv *httptest.Server, check func(netip.AddrPort) error) *http.Client {
	t.Helper()
	client := newSchemaClient(check)
	tlsCfg := srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	client.Transport.(*http.Transport).TLSClientConfig = tlsCfg
	return client
}

func TestFetchRejectsLoopbackCatalogURL(t *testing.T) {
	t.Parallel()
	srv, hits := countingServer(t, nil)

	store := &Store{
		entries:  []catalogEntry{{FileMatch: []string{"config.json"}, URL: srv.URL + "/schema.json"}},
		cacheDir: t.TempDir(),
		cacheTTL: defaultCacheTTL,
		client:   policyClient(t, srv, checkPublicAddr),
	}

	path, found, err := store.ResolveSchema("/project/config.json")
	require.True(t, found)
	require.Empty(t, path)
	require.ErrorIs(t, err, ErrBlockedURL)
	require.Equal(t, int32(0), hits.Load(), "loopback server must not receive a request")

	// Resolve must not hand the URL to the schema loader for a second request
	path, found = store.Resolve("/project/config.json")
	require.False(t, found)
	require.Empty(t, path)
	require.Equal(t, int32(0), hits.Load())
}

func TestFetchDefaultClientRejectsLoopback(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	store := &Store{
		entries:  []catalogEntry{{FileMatch: []string{"config.json"}, URL: srv.URL + "/schema.json"}},
		cacheDir: t.TempDir(),
		cacheTTL: defaultCacheTTL,
	}

	_, found, err := store.ResolveSchema("/project/config.json")
	require.True(t, found)
	require.ErrorIs(t, err, ErrBlockedURL)
	require.Equal(t, int32(0), hits.Load())
}

func TestFetchRejectsNonHTTPSCatalogURL(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	for _, u := range []string{srv.URL + "/schema.json", "file:///etc/passwd", "https:///schema.json"} {
		store := &Store{
			entries:  []catalogEntry{{FileMatch: []string{"config.json"}, URL: u}},
			cacheDir: t.TempDir(),
			cacheTTL: defaultCacheTTL,
			client:   srv.Client(),
		}
		path, found, err := store.ResolveSchema("/project/config.json")
		require.True(t, found, u)
		require.Empty(t, path, u)
		require.ErrorIs(t, err, ErrBlockedURL, u)
	}
	require.Equal(t, int32(0), hits.Load())
}

func TestFetchRejectsRedirectToLoopback(t *testing.T) {
	t.Parallel()
	internal, internalHits := countingServer(t, nil)
	allowed, allowedHits := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/admin", http.StatusFound)
	})

	store := &Store{
		entries:  []catalogEntry{{FileMatch: []string{"config.json"}, URL: allowed.URL + "/schema.json"}},
		cacheDir: t.TempDir(),
		cacheTTL: defaultCacheTTL,
		client:   policyClient(t, allowed, allowOnly(t, allowed)),
	}

	path, found, err := store.ResolveSchema("/project/config.json")
	require.True(t, found)
	require.Empty(t, path)
	require.ErrorIs(t, err, ErrBlockedURL)
	require.Equal(t, int32(1), allowedHits.Load())
	require.Equal(t, int32(0), internalHits.Load(), "redirect to loopback must not be followed")
}

func TestFetchRejectsRedirectToHTTP(t *testing.T) {
	t.Parallel()
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { plainHits.Add(1) }))
	defer plain.Close()
	allowed, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/schema.json", http.StatusFound)
	})

	store := &Store{
		entries:  []catalogEntry{{FileMatch: []string{"config.json"}, URL: allowed.URL + "/schema.json"}},
		cacheDir: t.TempDir(),
		cacheTTL: defaultCacheTTL,
		client: policyClient(t, allowed, func(netip.AddrPort) error {
			return nil
		}),
	}

	_, _, err := store.ResolveSchema("/project/config.json")
	require.ErrorIs(t, err, ErrBlockedURL)
	require.ErrorContains(t, err, "not https")
	require.Equal(t, int32(0), plainHits.Load())
}

func TestFetchFollowsAllowedRedirect(t *testing.T) {
	t.Parallel()
	allowed, hits := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old.json" {
			http.Redirect(w, r, "/new.json", http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write([]byte(`{"type":"object"}`))
	})

	store := &Store{
		entries:  []catalogEntry{{FileMatch: []string{"config.json"}, URL: allowed.URL + "/old.json"}},
		cacheDir: t.TempDir(),
		cacheTTL: defaultCacheTTL,
		client:   policyClient(t, allowed, allowOnly(t, allowed)),
	}

	path, found, err := store.ResolveSchema("/project/config.json")
	require.NoError(t, err)
	require.True(t, found)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"object"}`, string(data))
	require.Equal(t, int32(2), hits.Load())
}

func TestFetchStopsRedirectLoop(t *testing.T) {
	t.Parallel()
	allowed, hits := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	})

	store := &Store{
		entries:  []catalogEntry{{FileMatch: []string{"config.json"}, URL: allowed.URL + "/loop.json"}},
		cacheDir: t.TempDir(),
		cacheTTL: defaultCacheTTL,
		client:   policyClient(t, allowed, allowOnly(t, allowed)),
	}

	_, _, err := store.ResolveSchema("/project/config.json")
	require.ErrorIs(t, err, ErrBlockedURL)
	require.ErrorContains(t, err, "redirects")
	require.Equal(t, int32(maxRedirects), hits.Load())
}

func TestCheckPublicAddr(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"127.0.0.1:443", "10.0.0.1:443", "172.16.0.1:443", "192.168.1.1:443",
		"169.254.169.254:80", "0.0.0.0:443", "0.1.2.3:443", "100.64.0.1:443",
		"192.0.0.1:443", "198.18.0.1:443", "224.0.0.1:443", "240.0.0.1:443",
		"255.255.255.255:443", "[::1]:443", "[::]:443", "[fe80::1]:443",
		"[fd00::1]:443", "[ff02::1]:443", "[::ffff:127.0.0.1]:443", "[::ffff:169.254.169.254]:443",
	}
	for _, a := range blocked {
		err := checkPublicAddr(netip.MustParseAddrPort(a))
		require.ErrorIs(t, err, ErrBlockedURL, a)
	}
	for _, a := range []string{"104.16.0.1:443", "8.8.8.8:443", "[2606:4700::1]:443"} {
		require.NoError(t, checkPublicAddr(netip.MustParseAddrPort(a)), a)
	}
}

func TestCheckResolvedHost(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	u, err := url.Parse("https://127.0.0.1:8443/schema.json")
	require.NoError(t, err)
	require.ErrorIs(t, checkResolvedHost(ctx, u, checkPublicAddr), ErrBlockedURL)

	u, err = url.Parse("https://[::1]/schema.json")
	require.NoError(t, err)
	var seen netip.AddrPort
	require.NoError(t, checkResolvedHost(ctx, u, func(ap netip.AddrPort) error {
		seen = ap
		return nil
	}))
	require.Equal(t, uint16(443), seen.Port())

	u, err = url.Parse("https://name.invalid/schema.json")
	require.NoError(t, err)
	err = checkResolvedHost(ctx, u, checkPublicAddr)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrBlockedURL)
}
