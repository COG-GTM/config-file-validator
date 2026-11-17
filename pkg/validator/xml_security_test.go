package validator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/helium"
	"github.com/stretchr/testify/require"
)

const xxeCanary = "cfv-xxe-canary"

// writeOutsideFile writes content to a file in a directory unrelated to the
// scanned document and returns its file:// URI.
func writeOutsideFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0600))
	return "file://" + filepath.ToSlash(p)
}

func countingServer(t *testing.T, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func Test_XMLExternalDTDFileNotLoaded(t *testing.T) {
	t.Parallel()
	// If this DTD were loaded, the document below would be valid.
	dtd := writeOutsideFile(t, "outside.dtd", `<!ELEMENT root (#PCDATA)>`)
	doc := fmt.Sprintf(`<?xml version="1.0"?>
<!DOCTYPE root SYSTEM %q>
<root>x</root>`, dtd)

	valid, err := XMLValidator{}.ValidateSyntax([]byte(doc))
	require.False(t, valid)
	require.Error(t, err)
}

func Test_XMLExternalEntityFileNotLoaded(t *testing.T) {
	t.Parallel()
	secret := writeOutsideFile(t, "secret.txt", xxeCanary)
	doc := fmt.Sprintf(`<?xml version="1.0"?>
<!DOCTYPE root [
  <!ELEMENT root (#PCDATA)>
  <!ENTITY xxe SYSTEM %q>
]>
<root>&xxe;</root>`, secret)

	_, err := XMLValidator{}.ValidateSyntax([]byte(doc))
	if err != nil {
		require.NotContains(t, err.Error(), xxeCanary)
	}

	parsed, err := newXMLParser().ValidateDTD(true).Parse(context.Background(), []byte(doc))
	if err == nil {
		out, werr := helium.WriteString(parsed)
		require.NoError(t, werr)
		require.NotContains(t, out, xxeCanary)
	} else {
		require.NotContains(t, err.Error(), xxeCanary)
	}
}

func Test_XMLExternalReferencesNoNetwork(t *testing.T) {
	t.Parallel()
	srv, hits := countingServer(t, `<!ELEMENT root (#PCDATA)>`)
	docs := []string{
		fmt.Sprintf(`<?xml version="1.0"?>
<!DOCTYPE root SYSTEM %q>
<root>x</root>`, srv.URL+"/ext.dtd"),
		fmt.Sprintf(`<?xml version="1.0"?>
<!DOCTYPE root [
  <!ELEMENT root (#PCDATA)>
  <!ENTITY xxe SYSTEM %q>
]>
<root>&xxe;</root>`, srv.URL+"/entity.txt"),
		fmt.Sprintf(`<?xml version="1.0"?>
<!DOCTYPE root [
  <!ENTITY %% pe SYSTEM %q>
  %%pe;
]>
<root>x</root>`, srv.URL+"/pe.dtd"),
	}
	xsdFile := writeTestXSD(t)

	for _, doc := range docs {
		_, _ = XMLValidator{}.ValidateSyntax([]byte(doc))
		_, _ = ValidateXSD([]byte(doc), xsdFile)
	}
	require.Zero(t, hits.Load(), "validator made an outbound request for an external DTD or entity")
}

// billionLaughs returns a document whose single entity reference expands to
// 10^levels copies of "lol".
func billionLaughs(levels int) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\"?>\n<!DOCTYPE lolz [\n  <!ELEMENT lolz (#PCDATA)>\n")
	b.WriteString("  <!ENTITY lol0 \"lol\">\n")
	for i := 1; i <= levels; i++ {
		fmt.Fprintf(&b, "  <!ENTITY lol%d \"%s\">\n", i, strings.Repeat(fmt.Sprintf("&lol%d;", i-1), 10))
	}
	fmt.Fprintf(&b, "]>\n<lolz>&lol%d;</lolz>\n", levels)
	return b.String()
}

//nolint:paralleltest // measures process-wide allocations
func Test_XMLEntityExpansionBounded(t *testing.T) {
	doc := []byte(billionLaughs(10)) // ~30 GB if fully expanded
	xsdFile := writeTestXSD(t)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()

	valid, err := XMLValidator{}.ValidateSyntax(doc)
	require.False(t, valid)
	require.Error(t, err)

	valid, _ = ValidateXSD(doc, xsdFile)
	require.False(t, valid)

	runtime.ReadMemStats(&after)
	require.Less(t, time.Since(start), 10*time.Second)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(256<<20), "entity expansion allocated too much memory")
}

func Test_XMLSkipDTD(t *testing.T) {
	t.Parallel()
	doc := []byte(`<?xml version="1.0"?>
<!DOCTYPE config [
  <!ELEMENT config (host, port)>
  <!ELEMENT host (#PCDATA)>
  <!ELEMENT port (#PCDATA)>
]>
<config><host>db.example.com</host></config>`)

	valid, err := XMLValidator{}.ValidateSyntax(doc)
	require.False(t, valid)
	require.Error(t, err)

	valid, err = XMLValidator{SkipDTD: true}.ValidateSyntax(doc)
	require.True(t, valid)
	require.NoError(t, err)

	valid, err = XMLValidator{SkipDTD: true}.ValidateSyntax([]byte("<config><host>"))
	require.False(t, valid)
	require.Error(t, err)
}
