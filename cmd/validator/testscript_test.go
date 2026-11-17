package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"validator": main,
	})
}

func TestScript(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata",
		Setup: func(env *testscript.Env) error {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"type":"object"}`))
			}))
			env.Defer(srv.Close)
			env.Setenv("SCHEMA_SERVER", srv.URL)
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"mkcatalog": cmdMkcatalog,
		},
	})
}

// cmdMkcatalog writes a SchemaStore catalog to <bundle>/src/api/json/catalog.json
// with one entry matching <fileMatch> whose URL is $SCHEMA_SERVER<urlPath>.
// Usage: mkcatalog <bundle> <fileMatch> <urlPath>
func cmdMkcatalog(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 3 {
		ts.Fatalf("usage: mkcatalog <bundle> <fileMatch> <urlPath>")
	}
	cat := map[string]any{
		"schemas": []map[string]any{{
			"fileMatch": []string{args[1]},
			"url":       ts.Getenv("SCHEMA_SERVER") + args[2],
		}},
	}
	data, err := json.Marshal(cat)
	ts.Check(err)
	dir := filepath.Join(ts.MkAbs(args[0]), "src", "api", "json")
	ts.Check(os.MkdirAll(dir, 0o755))
	ts.Check(os.WriteFile(filepath.Join(dir, "catalog.json"), data, 0o600))
}
