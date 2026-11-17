package validator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/xeipuuv/gojsonreference"
	"github.com/xeipuuv/gojsonschema"

	"github.com/Boeing/config-file-validator/v2/pkg/tools"
)

// ErrSchemaLocationNotAllowed is returned when a schema location declared by
// the validated file itself (e.g. a JSON "$schema" property) or reached via
// "$ref" is outside the allowed locations.
var ErrSchemaLocationNotAllowed = errors.New("schema location not allowed")

// SchemaPolicy restricts which schema locations a validated file may declare
// for itself. Only https URLs that resolve to public addresses and local files
// inside one of Roots (after resolving symlinks) are loaded. The directory of
// the validated file and the current working directory are always allowed.
type SchemaPolicy struct {
	Roots []string
}

const (
	maxSchemaBytes     = 10 << 20
	maxSchemaRedirects = 10
	schemaFetchTimeout = 30 * time.Second
)

// Paths of the json-schema.org meta-schemas embedded in gojsonschema; loading
// them never touches the network.
var embeddedMetaSchemaPaths = map[string]bool{
	"/draft-04/schema": true,
	"/draft-06/schema": true,
	"/draft-07/schema": true,
}

// Address ranges that net/netip does not already classify as non-public.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
}

type schemaRoot struct {
	abs      string
	resolved string
}

// schemaLocator enforces a SchemaPolicy for one validated file.
type schemaLocator struct {
	roots  []schemaRoot
	client *http.Client
}

func newSchemaLocator(policy SchemaPolicy, filePath string) *schemaLocator {
	candidates := append([]string{".", filepath.Dir(filePath)}, policy.Roots...)
	l := &schemaLocator{client: schemaHTTPClient}
	seen := map[string]bool{}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			resolved = abs
		}
		l.roots = append(l.roots, schemaRoot{abs: abs, resolved: resolved})
	}
	return l
}

func notAllowed(location, reason string) error {
	return fmt.Errorf("%w: %q %s", ErrSchemaLocationNotAllowed, location, reason)
}

// resolve turns a schema reference declared in the file at filePath into a
// URL and checks it against the policy without loading it.
func (l *schemaLocator) resolve(schemaURL, filePath string) (string, error) {
	if filepath.IsAbs(schemaURL) {
		if _, err := l.localPath(schemaURL, schemaURL); err != nil {
			return "", err
		}
		return tools.FileURL(schemaURL), nil
	}

	parsed, err := url.Parse(schemaURL)
	if err == nil && parsed.Scheme != "" {
		if err := l.checkURL(parsed, schemaURL); err != nil {
			return "", err
		}
		return schemaURL, nil
	}

	absSchema, err := filepath.Abs(filepath.Join(filepath.Dir(filePath), schemaURL))
	if err != nil {
		return "", notAllowed(schemaURL, "cannot be resolved")
	}
	if _, err := l.localPath(absSchema, schemaURL); err != nil {
		return "", err
	}
	return tools.FileURL(absSchema), nil
}

func (l *schemaLocator) checkURL(u *url.URL, location string) error {
	switch {
	case isEmbeddedMetaSchema(u):
		return nil
	case strings.EqualFold(u.Scheme, "https"):
		if u.Hostname() == "" {
			return notAllowed(location, "has no host")
		}
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !isPublicAddr(ip) {
			return notAllowed(location, "targets a non-public address")
		}
		return nil
	case strings.EqualFold(u.Scheme, "file"):
		p, err := fileURLToPath(u)
		if err != nil {
			return notAllowed(location, "is not a local file URL")
		}
		_, err = l.localPath(p, location)
		return err
	default:
		return notAllowed(location, "must use https or be a local file inside the scan root")
	}
}

// localPath returns the symlink-resolved path of p if p is an existing file
// inside an allowed root. Missing files and files outside the roots get the
// same error so the error text does not reveal which files exist.
func (l *schemaLocator) localPath(p, location string) (string, error) {
	outside := notAllowed(location, "is not an existing file inside the scan root")
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", outside
	}
	lexicalOK := false
	for _, r := range l.roots {
		if within(r.abs, abs) || within(r.resolved, abs) {
			lexicalOK = true
			break
		}
	}
	if !lexicalOK {
		return "", outside
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", outside
	}
	for _, r := range l.roots {
		if within(r.resolved, resolved) {
			return resolved, nil
		}
	}
	return "", outside
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isEmbeddedMetaSchema(u *url.URL) bool {
	return u.Scheme == "http" && u.Host == "json-schema.org" && u.RawQuery == "" && embeddedMetaSchemaPaths[u.Path]
}

func fileURLToPath(u *url.URL) (string, error) {
	host := u.Host
	if host == "localhost" {
		host = ""
	}
	if runtime.GOOS == "windows" {
		if host != "" {
			return `\\` + host + filepath.FromSlash(u.Path), nil
		}
		return filepath.FromSlash(strings.TrimPrefix(u.Path, "/")), nil
	}
	if host != "" {
		return "", errors.New("remote file URL")
	}
	return u.Path, nil
}

func isPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// load fetches the document at location after checking it against the policy.
func (l *schemaLocator) load(location string) (any, error) {
	u, err := url.Parse(location)
	if err != nil {
		return nil, notAllowed(location, "is not a valid URL")
	}
	u.Fragment = ""
	u.RawFragment = ""
	if err := l.checkURL(u, location); err != nil {
		return nil, err
	}

	if isEmbeddedMetaSchema(u) {
		return gojsonschema.NewReferenceLoader(u.String()).LoadJSON()
	}

	var body []byte
	if strings.EqualFold(u.Scheme, "file") {
		p, _ := fileURLToPath(u)
		resolved, err := l.localPath(p, location)
		if err != nil {
			return nil, err
		}
		body, err = readLimited(resolved)
		if err != nil {
			return nil, err
		}
	} else {
		body, err = l.fetch(u.String())
		if err != nil {
			return nil, err
		}
	}
	return gojsonschema.NewBytesLoader(body).LoadJSON()
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path is checked by localPath to be inside an allowed root
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readAllLimited(f, path)
}

func readAllLimited(r io.Reader, location string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxSchemaBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSchemaBytes {
		return nil, fmt.Errorf("schema %q exceeds %d bytes", location, maxSchemaBytes)
	}
	return b, nil
}

func (l *schemaLocator) fetch(location string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), schemaFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		var nae *notAllowedDialError
		if errors.As(err, &nae) {
			return nil, notAllowed(location, "resolves to a non-public address")
		}
		if errors.Is(err, ErrSchemaLocationNotAllowed) {
			return nil, err
		}
		return nil, fmt.Errorf("fetching schema %q: %w", location, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching schema %q: unexpected status %s", location, resp.Status)
	}
	return readAllLimited(resp.Body, location)
}

type notAllowedDialError struct{ addr string }

func (e *notAllowedDialError) Error() string {
	return "connection to non-public address " + e.addr + " refused"
}

// guardDialControl runs after DNS resolution for every connection, so it also
// covers redirects and DNS rebinding.
func guardDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return &notAllowedDialError{addr: address}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !isPublicAddr(ip) {
		return &notAllowedDialError{addr: address}
	}
	return nil
}

func newSchemaHTTPClient(control func(network, address string, c syscall.RawConn) error) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: control}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: schemaFetchTimeout,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   schemaFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxSchemaRedirects {
				return fmt.Errorf("stopped after %d redirects", maxSchemaRedirects)
			}
			if !strings.EqualFold(req.URL.Scheme, "https") {
				return notAllowed(req.URL.String(), "redirect must use https")
			}
			return nil
		},
	}
}

// schemaHTTPClient connects directly (no proxy) so the dial guard sees the
// resolved destination address.
var schemaHTTPClient = newSchemaHTTPClient(guardDialControl)

// policyLoader is a gojsonschema.JSONLoader that applies a schemaLocator to
// the root schema and, through LoaderFactory, to every "$ref" target.
type policyLoader struct {
	source  string
	locator *schemaLocator
}

var _ gojsonschema.JSONLoader = (*policyLoader)(nil)

func (l *policyLoader) JsonSource() any { //nolint:revive,staticcheck // name is fixed by gojsonschema.JSONLoader
	return l.source
}

func (l *policyLoader) LoadJSON() (any, error) {
	return l.locator.load(l.source)
}

func (l *policyLoader) JsonReference() (gojsonreference.JsonReference, error) { //nolint:revive,staticcheck // name is fixed by gojsonschema.JSONLoader
	return gojsonreference.NewJsonReference(l.source)
}

func (l *policyLoader) LoaderFactory() gojsonschema.JSONLoaderFactory {
	return policyLoaderFactory{locator: l.locator}
}

type policyLoaderFactory struct {
	locator *schemaLocator
}

func (f policyLoaderFactory) New(source string) gojsonschema.JSONLoader {
	return &policyLoader{source: source, locator: f.locator}
}

// validateDeclaredSchema validates docJSON against a schema that the file at
// filePath declared for itself, enforcing policy on the schema and its refs.
func validateDeclaredSchema(schemaURL, filePath string, policy SchemaPolicy, docJSON []byte, posMap map[string]SourcePosition) (bool, error) {
	locator := newSchemaLocator(policy, filePath)
	resolved, err := locator.resolve(schemaURL, filePath)
	if err != nil {
		return false, fmt.Errorf("schema validation error: %w", err)
	}
	return validateWithLoader(&policyLoader{source: resolved, locator: locator}, docJSON, posMap)
}
