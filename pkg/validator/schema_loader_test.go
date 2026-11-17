package validator

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Boeing/config-file-validator/v2/pkg/tools"
)

func setSchemaLimits(t *testing.T, timeout time.Duration, maxBytes int64) {
	t.Helper()
	origTimeout, origMax := schemaFetchTimeout, schemaMaxBytes
	schemaFetchTimeout, schemaMaxBytes = timeout, maxBytes
	t.Cleanup(func() {
		schemaFetchTimeout, schemaMaxBytes = origTimeout, origMax
	})
}

func requireReturnsWithin(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("schema load did not return within %s", d)
	}
}

func TestSchemaLoaderRejectsDeviceFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/dev/zero is not available on Windows")
	}
	t.Parallel()

	doc := []byte(`{"$schema": "/dev/zero"}`)
	var (
		valid bool
		err   error
	)
	requireReturnsWithin(t, 10*time.Second, func() {
		valid, err = JSONValidator{}.ValidateSchema(doc, filepath.Join(t.TempDir(), "x.json"))
	})
	require.False(t, valid)
	require.ErrorContains(t, err, "not a regular file")
}

func TestSchemaLoaderRejectsDeviceFileRef(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/dev/zero is not available on Windows")
	}
	t.Parallel()

	dir := t.TempDir()
	schema := `{"$ref": "file:///dev/zero"}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.json"), []byte(schema), 0600))

	var (
		valid bool
		err   error
	)
	requireReturnsWithin(t, 10*time.Second, func() {
		valid, err = JSONValidator{}.ValidateSchema([]byte(`{"$schema": "schema.json"}`), filepath.Join(dir, "x.json"))
	})
	require.False(t, valid)
	require.ErrorContains(t, err, "not a regular file")
}

func TestSchemaLoaderRejectsDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	valid, err := JSONSchemaValidate(tools.FileURL(dir), []byte(`{}`))
	require.False(t, valid)
	require.ErrorContains(t, err, "not a regular file")
}

func TestSchemaLoaderRejectsOversizedFile(t *testing.T) {
	setSchemaLimits(t, schemaFetchTimeout, 64)

	p := filepath.Join(t.TempDir(), "big.json")
	big := `{"description": "` + strings.Repeat("a", 128) + `"}`
	require.NoError(t, os.WriteFile(p, []byte(big), 0600))

	valid, err := JSONSchemaValidate(tools.FileURL(p), []byte(`{}`))
	require.False(t, valid)
	require.ErrorIs(t, err, errSchemaTooLarge)
}

func TestSchemaLoaderHTTPTimeout(t *testing.T) {
	setSchemaLimits(t, 200*time.Millisecond, schemaMaxBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	var (
		valid bool
		err   error
	)
	start := time.Now()
	requireReturnsWithin(t, 10*time.Second, func() {
		valid, err = JSONSchemaValidate(srv.URL+"/schema.json", []byte(`{}`))
	})
	require.False(t, valid)
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestSchemaLoaderHTTPEndlessBody(t *testing.T) {
	setSchemaLimits(t, 30*time.Second, 1<<20)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"description": "`))
		chunk := []byte(strings.Repeat("a", 32<<10))
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	var (
		valid bool
		err   error
	)
	requireReturnsWithin(t, 10*time.Second, func() {
		valid, err = JSONSchemaValidate(srv.URL+"/schema.json", []byte(`{}`))
	})
	require.False(t, valid)
	require.ErrorIs(t, err, errSchemaTooLarge)
}

func TestSchemaLoaderHTTP(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/schema.json":
			_, _ = w.Write([]byte(`{"type": "object", "properties": {"port": {"$ref": "defs.json#/port"}}}`))
		case "/defs.json":
			_, _ = w.Write([]byte(`{"port": {"type": "integer"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	valid, err := JSONSchemaValidate(srv.URL+"/schema.json", []byte(`{"port": 5432}`))
	require.NoError(t, err)
	require.True(t, valid)

	valid, err = JSONSchemaValidate(srv.URL+"/schema.json", []byte(`{"port": "x"}`))
	require.False(t, valid)
	require.ErrorContains(t, err, "schema validation failed")

	valid, err = JSONSchemaValidate(srv.URL+"/missing.json", []byte(`{}`))
	require.False(t, valid)
	require.ErrorContains(t, err, "HTTP 404")
}

func TestSchemaLoaderBundledMetaSchemaRef(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "schema.json")
	require.NoError(t, os.WriteFile(p, []byte(`{"$ref": "http://json-schema.org/draft-07/schema#"}`), 0600)) // DevSkim: ignore DS137138

	valid, err := JSONSchemaValidate(tools.FileURL(p), []byte(`{"type": "object"}`))
	require.NoError(t, err)
	require.True(t, valid)

	valid, err = JSONSchemaValidate(tools.FileURL(p), []byte(`{"type": 5}`))
	require.False(t, valid)
	require.ErrorContains(t, err, "schema validation failed")
}

func TestSchemaLoaderUnsupportedScheme(t *testing.T) {
	t.Parallel()

	valid, err := JSONSchemaValidate("ftp://example.com/schema.json", []byte(`{}`))
	require.False(t, valid)
	require.ErrorContains(t, err, "unsupported schema URL scheme")
}

func TestSchemaLoaderInvalidJSON(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "schema.json")
	require.NoError(t, os.WriteFile(p, []byte(`{not json`), 0600))

	valid, err := JSONSchemaValidate(tools.FileURL(p), []byte(`{}`))
	require.False(t, valid)
	require.Error(t, err)
}
