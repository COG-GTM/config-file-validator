package validator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Boeing/config-file-validator/v2/pkg/tools"
)

func newSchemaServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/schema.json":
			_, _ = w.Write([]byte(`{"type":"object","properties":{"port":{"$ref":"defs.json#/port"}},"required":["port"]}`))
		case "/defs.json":
			_, _ = w.Write([]byte(`{"port":{"type":"integer"}}`))
		case "/big.json":
			_, _ = w.Write([]byte(`{"description":"` + strings.Repeat("a", tools.MaxRemoteSchemaSize) + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func Test_JSONSchemaValidateRemoteSchema(t *testing.T) {
	t.Parallel()
	srv := newSchemaServer(t)

	valid, err := JSONSchemaValidate(srv.URL+"/schema.json", []byte(`{"port":5432}`))
	require.True(t, valid)
	require.NoError(t, err)

	valid, err = JSONSchemaValidate(srv.URL+"/schema.json", []byte(`{"port":"x"}`))
	require.False(t, valid)
	require.ErrorContains(t, err, "schema validation failed")
}

func Test_JSONSchemaValidateRemoteSchemaTooLarge(t *testing.T) {
	t.Parallel()
	srv := newSchemaServer(t)

	valid, err := JSONSchemaValidate(srv.URL+"/big.json", []byte(`{}`))
	require.False(t, valid)
	require.ErrorIs(t, err, tools.ErrRemoteSchemaTooLarge)
}

func Test_JSONSchemaValidateRemoteSchemaNotFound(t *testing.T) {
	t.Parallel()
	srv := newSchemaServer(t)

	valid, err := JSONSchemaValidate(srv.URL+"/missing.json", []byte(`{}`))
	require.False(t, valid)
	require.ErrorContains(t, err, "HTTP 404")
}
