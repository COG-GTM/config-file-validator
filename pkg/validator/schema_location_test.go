package validator

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

const locationTestSchema = `{"type":"object","properties":{"port":{"type":"integer"}}}`

type countingServer struct {
	*httptest.Server
	hits atomic.Int64
}

func newCountingServer(t *testing.T, start func(http.Handler) *httptest.Server, h http.HandlerFunc) *countingServer {
	t.Helper()
	cs := &countingServer{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.hits.Add(1)
		if h != nil {
			h(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"type":"object","properties":{"port":{"enum":["secret-value"]}}}`))
	})
	cs.Server = start(handler)
	t.Cleanup(cs.Close)
	return cs
}

type declaredSchemaCase struct {
	name string
	v    PolicySchemaValidator
	file string
	doc  func(schema string) string
}

var declaredSchemaCases = []declaredSchemaCase{
	{"json", JSONValidator{}, "probe.json", func(s string) string { return fmt.Sprintf(`{"$schema": %q, "port": 1}`, s) }},
	{"jsonc", JSONCValidator{}, "probe.jsonc", func(s string) string { return fmt.Sprintf("// c\n{\"$schema\": %q, \"port\": 1}", s) }},
	{"yaml", YAMLValidator{}, "probe.yaml", func(s string) string { return "# yaml-language-server: $schema=" + s + "\nport: 1\n" }},
	{"toml", TomlValidator{}, "probe.toml", func(s string) string { return fmt.Sprintf("\"$schema\" = %q\nport = 1\n", s) }},
	{"toon", ToonValidator{}, "probe.toon", func(s string) string { return fmt.Sprintf("\"$schema\": %q\nport: 1\n", s) }},
}

func requireNotAllowed(t *testing.T, valid bool, err error) {
	t.Helper()
	require.False(t, valid)
	require.ErrorIs(t, err, ErrSchemaLocationNotAllowed)
	require.Contains(t, err.Error(), "schema location not allowed")
	require.NotContains(t, err.Error(), "secret-value")
}

func Test_DeclaredSchemaRejectsDisallowedLocations(t *testing.T) {
	t.Parallel()
	plain := newCountingServer(t, httptest.NewServer, nil)
	secure := newCountingServer(t, httptest.NewTLSServer, nil)
	_, securePort, _ := strings.Cut(strings.TrimPrefix(secure.URL, "https://"), ":")

	base := t.TempDir()
	root := filepath.Join(base, "repo")
	dir := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	outside := filepath.Join(base, "outside.json")
	require.NoError(t, os.WriteFile(outside, []byte(locationTestSchema), 0o600))

	locations := []string{
		plain.URL + "/x",
		"http://10.0.0.5:8500/v1/agent/self",
		"http://169.254.169.254/latest/meta-data/",
		secure.URL + "/x",
		"https://localhost:" + securePort + "/x",
		"https://[::1]:" + securePort + "/x",
		"ftp://example.com/schema.json",
		"gopher://127.0.0.1:6379/_INFO",
		"file:///etc/hostname",
		"/etc/hostname",
		"file://" + filepath.ToSlash(outside),
		outside,
		"../../../outside.json",
		"../../../outside-missing.json",
	}
	if runtime.GOOS == "windows" {
		locations = append(locations, `C:\Windows\win.ini`)
	}

	for _, tc := range declaredSchemaCases {
		for _, loc := range locations {
			t.Run(tc.name+"/"+loc, func(t *testing.T) {
				t.Parallel()
				valid, err := tc.v.ValidateSchemaWithPolicy([]byte(tc.doc(loc)), filepath.Join(dir, tc.file), SchemaPolicy{Roots: []string{root}})
				requireNotAllowed(t, valid, err)
			})
		}
	}

	t.Cleanup(func() {
		require.Zero(t, plain.hits.Load(), "plain test server must receive no requests")
		require.Zero(t, secure.hits.Load(), "TLS test server must receive no requests")
	})
}

func Test_DeclaredSchemaDefaultPolicyRejectsOutsideFile(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	dir := filepath.Join(base, "inner")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "outside.json"), []byte(locationTestSchema), 0o600))

	valid, err := JSONValidator{}.ValidateSchema([]byte(`{"$schema": "../outside.json", "port": 1}`), filepath.Join(dir, "c.json"))
	requireNotAllowed(t, valid, err)
}

func Test_DeclaredSchemaAllowsFilesInsideRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "configs", "app")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "schemas"), 0o755))
	schema := filepath.Join(root, "schemas", "s.json")
	require.NoError(t, os.WriteFile(schema, []byte(locationTestSchema), 0o600))

	policy := SchemaPolicy{Roots: []string{root}}
	for _, loc := range []string{"../../schemas/s.json", schema} {
		for _, tc := range declaredSchemaCases {
			valid, err := tc.v.ValidateSchemaWithPolicy([]byte(tc.doc(loc)), filepath.Join(dir, tc.file), policy)
			require.NoError(t, err, "%s %s", tc.name, loc)
			require.True(t, valid, "%s %s", tc.name, loc)
		}
	}

	valid, err := JSONValidator{}.ValidateSchemaWithPolicy([]byte(`{"$schema": "../../schemas/s.json", "port": "x"}`), filepath.Join(dir, "c.json"), policy)
	require.False(t, valid)
	var se *SchemaErrors
	require.ErrorAs(t, err, &se)
}

func Test_DeclaredSchemaRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	require.NoError(t, os.MkdirAll(root, 0o755))
	outside := filepath.Join(base, "outside.json")
	require.NoError(t, os.WriteFile(outside, []byte(locationTestSchema), 0o600))
	if err := os.Symlink(outside, filepath.Join(root, "link.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	valid, err := JSONValidator{}.ValidateSchemaWithPolicy([]byte(`{"$schema": "link.json", "port": 1}`), filepath.Join(root, "c.json"), SchemaPolicy{Roots: []string{root}})
	requireNotAllowed(t, valid, err)
}

func Test_DeclaredSchemaRefsFollowPolicy(t *testing.T) {
	t.Parallel()
	srv := newCountingServer(t, httptest.NewServer, nil)
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "outside.json"), []byte(locationTestSchema), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "inner.json"), []byte(locationTestSchema), 0o600))

	write := func(name, ref string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), fmt.Appendf(nil, `{"$ref": %q}`, ref), 0o600))
	}
	write("ref-http.json", srv.URL+"/x")
	write("ref-outside.json", "../outside.json")
	write("ref-inside.json", "inner.json")

	policy := SchemaPolicy{Roots: []string{root}}
	file := filepath.Join(root, "c.json")
	for _, s := range []string{"ref-http.json", "ref-outside.json"} {
		valid, err := JSONValidator{}.ValidateSchemaWithPolicy(fmt.Appendf(nil, `{"$schema": %q, "port": 1}`, s), file, policy)
		requireNotAllowed(t, valid, err)
	}
	require.Zero(t, srv.hits.Load())

	valid, err := JSONValidator{}.ValidateSchemaWithPolicy([]byte(`{"$schema": "ref-inside.json", "port": 1}`), file, policy)
	require.NoError(t, err)
	require.True(t, valid)
}

func Test_DeclaredSchemaEmbeddedMetaSchema(t *testing.T) {
	t.Parallel()
	doc := `{"$schema": "http://json-schema.org/draft-07/schema#", "type": "object"}`
	valid, err := JSONValidator{}.ValidateSchema([]byte(doc), "schema.json")
	require.NoError(t, err)
	require.True(t, valid)

	valid, err = JSONValidator{}.ValidateSchema([]byte(`{"$schema": "http://json-schema.org/draft-07/schema#", "type": 5}`), "schema.json")
	require.False(t, valid)
	var se *SchemaErrors
	require.ErrorAs(t, err, &se)
}

func allowAllDial(string, string, syscall.RawConn) error { return nil }

func testLocator(t *testing.T, srv *httptest.Server, control func(string, string, syscall.RawConn) error) *schemaLocator {
	t.Helper()
	l := newSchemaLocator(SchemaPolicy{}, "c.json")
	l.client = newSchemaHTTPClient(control)
	tr, ok := l.client.Transport.(*http.Transport)
	require.True(t, ok)
	srvTransport, ok := srv.Client().Transport.(*http.Transport)
	require.True(t, ok)
	tr.TLSClientConfig = &tls.Config{RootCAs: srvTransport.TLSClientConfig.RootCAs, ServerName: "example.com", MinVersion: tls.VersionTLS12}
	return l
}

func Test_SchemaFetch(t *testing.T) {
	t.Parallel()
	var srv *countingServer
	srv = newCountingServer(t, httptest.NewTLSServer, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(locationTestSchema))
		case "/redirect-https":
			http.Redirect(w, r, srv.URL+"/ok", http.StatusFound)
		case "/redirect-http":
			http.Redirect(w, r, "http://"+strings.TrimPrefix(srv.URL, "https://")+"/ok", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat(" ", maxSchemaBytes+1)))
		default:
			http.NotFound(w, r)
		}
	})
	l := testLocator(t, srv.Server, allowAllDial)
	_, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "https://"), ":")
	base := "https://localhost:" + port

	doc, err := l.load(base + "/ok")
	require.NoError(t, err)
	require.NotNil(t, doc)

	_, err = l.load(base + "/redirect-https")
	require.NoError(t, err)

	_, err = l.load(base + "/redirect-http")
	require.ErrorIs(t, err, ErrSchemaLocationNotAllowed)

	_, err = l.load(base + "/loop")
	require.ErrorContains(t, err, "redirects")

	_, err = l.load(base + "/missing")
	require.ErrorContains(t, err, "unexpected status")

	_, err = l.load(base + "/big")
	require.ErrorContains(t, err, "exceeds")
}

func Test_SchemaFetchDialGuard(t *testing.T) {
	t.Parallel()
	srv := newCountingServer(t, httptest.NewTLSServer, nil)
	l := testLocator(t, srv.Server, guardDialControl)

	_, err := l.fetch(srv.URL + "/x")
	require.ErrorIs(t, err, ErrSchemaLocationNotAllowed)
	require.Zero(t, srv.hits.Load())
}

func Test_isPublicAddr(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]bool{
		"8.8.8.8":          true,
		"2606:4700::1111":  true,
		"127.0.0.1":        false,
		"10.1.2.3":         false,
		"172.16.0.1":       false,
		"192.168.1.1":      false,
		"169.254.169.254":  false,
		"100.64.0.1":       false,
		"0.0.0.0":          false,
		"::1":              false,
		"fe80::1":          false,
		"fd00::1":          false,
		"::ffff:127.0.0.1": false,
		"64:ff9b::a00:1":   false,
		"224.0.0.1":        false,
		"255.255.255.255":  false,
	} {
		require.Equal(t, want, isPublicAddr(netip.MustParseAddr(addr)), addr)
	}
}

func Test_guardDialControl(t *testing.T) {
	t.Parallel()
	require.NoError(t, guardDialControl("tcp", "8.8.8.8:443", nil))
	require.Error(t, guardDialControl("tcp", "127.0.0.1:443", nil))
	require.Error(t, guardDialControl("tcp", "bad", nil))
}

func Test_fileURLToPath(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file URL semantics")
	}
	p, err := fileURLToPath(&url.URL{Scheme: "file", Host: "localhost", Path: "/tmp/x.json"})
	require.NoError(t, err)
	require.Equal(t, "/tmp/x.json", p)
	_, err = fileURLToPath(&url.URL{Scheme: "file", Host: "evil.example", Path: "/x.json"})
	require.Error(t, err)

	l := newSchemaLocator(SchemaPolicy{}, "c.json")
	_, err = l.resolve("file://evil.example/x.json", "c.json")
	require.ErrorIs(t, err, ErrSchemaLocationNotAllowed)
	_, err = l.resolve("https:///nohost", "c.json")
	require.ErrorIs(t, err, ErrSchemaLocationNotAllowed)
	_, err = l.load("%zz")
	require.ErrorIs(t, err, ErrSchemaLocationNotAllowed)
}
