package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchRemoteSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{
			name: "valid schema",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"type":"object"}`))
			},
		},
		{
			name: "non-200 status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantErr: "HTTP 404",
		},
		{
			name: "body larger than cap",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat(" ", MaxRemoteSchemaSize) + "{}"))
			},
			wantErr: "exceeds",
		},
		{
			name: "invalid JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"type":`))
			},
			wantErr: "not valid JSON",
		},
		{
			name: "connection closed mid-body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "1000")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"type":`))
				w.(http.Flusher).Flush()
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
			},
			wantErr: "reading schema",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			body, err := FetchRemoteSchema(srv.URL + "/schema.json")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, body)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, `{"type":"object"}`, string(body))
		})
	}
}

func TestFetchRemoteSchemaBadURL(t *testing.T) {
	t.Parallel()
	_, err := FetchRemoteSchema("http://[::1]:namedport/")
	require.Error(t, err)
	_, err = FetchRemoteSchema("http://127.0.0.1:1/unreachable.json") // DevSkim: ignore DS162092 - test server address, not debug code
	require.Error(t, err)
}
