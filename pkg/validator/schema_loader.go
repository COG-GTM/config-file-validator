package validator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xeipuuv/gojsonschema"
)

const maxSchemaBytes int64 = 10 << 20

var (
	schemaFetchTimeout = 30 * time.Second
	schemaMaxBytes     = maxSchemaBytes
)

var errSchemaTooLarge = errors.New("schema exceeds size limit")

// boundedSchemaLoader loads JSON schemas (and every $ref they pull in) with a
// byte cap, a fetch timeout and a regular-file check, since schema locations
// come from the untrusted files being validated.
type boundedSchemaLoader struct {
	gojsonschema.JSONLoader
	source string
}

type boundedSchemaLoaderFactory struct{}

func newBoundedSchemaLoader(source string) gojsonschema.JSONLoader {
	return &boundedSchemaLoader{
		JSONLoader: gojsonschema.NewReferenceLoader(source),
		source:     source,
	}
}

func (boundedSchemaLoaderFactory) New(source string) gojsonschema.JSONLoader {
	return newBoundedSchemaLoader(source)
}

func (*boundedSchemaLoader) LoaderFactory() gojsonschema.JSONLoaderFactory {
	return boundedSchemaLoaderFactory{}
}

func (l *boundedSchemaLoader) LoadJSON() (any, error) {
	u, err := url.Parse(l.source)
	if err != nil {
		return nil, err
	}
	u.Fragment = ""

	switch strings.ToLower(u.Scheme) {
	case "file":
		return loadSchemaFile(fileURLPath(u))
	case "http", "https":
		if isBundledMetaSchema(u) {
			return l.JSONLoader.LoadJSON()
		}
		return loadSchemaHTTP(u.String())
	default:
		return nil, fmt.Errorf("unsupported schema URL scheme %q", u.Scheme)
	}
}

func fileURLPath(u *url.URL) string {
	p := u.Path
	if runtime.GOOS == "windows" {
		if u.Host != "" {
			return filepath.FromSlash("//" + u.Host + p)
		}
		p = strings.TrimPrefix(p, "/")
		return filepath.FromSlash(p)
	}
	return p
}

// isBundledMetaSchema reports whether gojsonschema serves u from its embedded
// draft-04/06/07 meta-schemas without any network access.
func isBundledMetaSchema(u *url.URL) bool {
	if u.Host != "json-schema.org" {
		return false
	}
	switch strings.TrimSuffix(u.Path, "/") {
	case "/draft-04/schema", "/draft-06/schema", "/draft-07/schema":
		return true
	}
	return false
}

func loadSchemaFile(path string) (any, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("schema %s is not a regular file", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("schema %s is not a regular file", path)
	}

	return decodeBoundedSchema(f)
}

func loadSchemaHTTP(address string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), schemaFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching schema %s: HTTP %d", address, resp.StatusCode)
	}

	return decodeBoundedSchema(resp.Body)
}

func decodeBoundedSchema(r io.Reader) (any, error) {
	body, err := io.ReadAll(io.LimitReader(r, schemaMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > schemaMaxBytes {
		return nil, fmt.Errorf("%w of %d bytes", errSchemaTooLarge, schemaMaxBytes)
	}

	var doc any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	return doc, nil
}
