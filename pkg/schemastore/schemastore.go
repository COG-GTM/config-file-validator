package schemastore

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/Boeing/config-file-validator/v2/pkg/tools"
)

const defaultCacheTTL = 24 * time.Hour

//go:embed catalog.json
var embeddedCatalog []byte

var supportedExts = map[string]struct{}{
	"json": {},
	"yaml": {},
	"yml":  {},
	"toml": {},
	"toon": {},
}

type catalogEntry struct {
	FileMatch []string `json:"fileMatch"`
	URL       string   `json:"url"`
}

type catalog struct {
	Schemas []catalogEntry `json:"schemas"`
}

// Store holds a parsed SchemaStore catalog and resolves file paths to schema locations.
// When backed by a local clone, schemas resolve to local files first.
// Otherwise, schemas are fetched remotely and cached locally.
type Store struct {
	entries   []catalogEntry
	basePath  string
	schemaDir string
	cacheDir  string
	cacheTTL  time.Duration
}

// Open reads the SchemaStore catalog from bundlePath and returns a Store.
// The bundlePath should be the root of a SchemaStore clone or download
// containing src/api/json/catalog.json and src/schemas/json/*.json.
func Open(bundlePath string) (*Store, error) {
	catalogPath := filepath.Join(bundlePath, "src", "api", "json", "catalog.json")
	data, err := os.ReadFile(catalogPath)
	if err != nil {
		return nil, fmt.Errorf("reading schemastore catalog: %w", err)
	}

	return newStore(data, bundlePath)
}

// OpenEmbedded creates a Store from the embedded SchemaStore catalog.
// Schemas resolve to remote URLs since no local clone is available.
func OpenEmbedded() (*Store, error) {
	return newStore(embeddedCatalog, "")
}

func newStore(data []byte, bundlePath string) (*Store, error) {
	var cat catalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return nil, fmt.Errorf("parsing schemastore catalog: %w", err)
	}

	filtered := make([]catalogEntry, 0, len(cat.Schemas))
	for _, entry := range cat.Schemas {
		if hasSupported(entry.FileMatch) {
			filtered = append(filtered, entry)
		}
	}

	s := &Store{
		entries:  filtered,
		cacheTTL: defaultCacheTTL,
	}
	if bundlePath != "" {
		s.basePath = bundlePath
		s.schemaDir = filepath.Join(bundlePath, "src", "schemas", "json")
	}

	cacheDir, err := defaultCacheDir()
	if err == nil {
		s.cacheDir = cacheDir
	}

	return s, nil
}

// Lookup matches a file path against the catalog and returns a schema location.
// Resolution order: local clone → cache → remote URL.
// Remote schemas are cached locally for subsequent runs.
func (s *Store) Resolve(filePath string) (string, bool) {
	name := filepath.Base(filePath)
	for _, entry := range s.entries {
		for _, pattern := range entry.FileMatch {
			matched, err := matchPattern(pattern, filePath, name)
			if err != nil || !matched {
				continue
			}
			// Try local clone first
			if s.schemaDir != "" {
				if localPath := s.resolveLocal(entry.URL); localPath != "" {
					if _, err := os.Stat(localPath); err == nil {
						return localPath, true
					}
				}
			}
			if entry.URL == "" {
				continue
			}
			// Try cache
			if cached, ok := s.lookupCache(entry.URL); ok {
				return cached, true
			}
			// Fetch and cache
			if cached, err := s.fetchAndCache(entry.URL); err == nil {
				return cached, true
			}
			// Fall back to remote URL (no cache available)
			return entry.URL, true
		}
	}
	return "", false
}

func (s *Store) resolveLocal(schemaURL string) string {
	const host = "www.schemastore.org"
	if !strings.Contains(schemaURL, host) {
		return ""
	}
	// URL format: https://www.schemastore.org/foo.json -> src/schemas/json/foo.json
	idx := strings.LastIndex(schemaURL, "/")
	if idx < 0 {
		return ""
	}
	filename := schemaURL[idx+1:]
	return filepath.Join(s.schemaDir, filename)
}

func matchPattern(pattern, filePath, baseName string) (bool, error) {
	// Patterns like "package.json" (no glob) match against the base name
	if !strings.ContainsAny(pattern, "*?[") {
		if pattern == baseName {
			return true, nil
		}
		return false, nil
	}
	// Glob patterns match against the full path
	return doublestar.PathMatch(pattern, filePath)
}

func defaultCacheDir() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "cfv", "schemas"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "cfv", "schemas"), nil
}

// cachePathForURL converts a schema URL to a local cache file path.
// e.g. https://www.schemastore.org/package.json → <cacheDir>/www.schemastore.org/package.json
func (s *Store) cachePathForURL(schemaURL string) (string, error) {
	if s.cacheDir == "" {
		return "", errors.New("no cache directory")
	}
	parsed, err := url.Parse(schemaURL)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.cacheDir, parsed.Host, parsed.Path), nil
}

func (s *Store) lookupCache(schemaURL string) (string, bool) {
	cachePath, err := s.cachePathForURL(schemaURL)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(cachePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > tools.MaxRemoteSchemaSize {
		return "", false
	}
	if time.Since(info.ModTime()) > s.cacheTTL {
		return "", false
	}
	data, err := os.ReadFile(cachePath)
	if err != nil || !json.Valid(data) {
		return "", false
	}
	return cachePath, true
}

func (s *Store) fetchAndCache(schemaURL string) (string, error) {
	cachePath, err := s.cachePathForURL(schemaURL)
	if err != nil {
		return "", err
	}

	data, err := tools.FetchRemoteSchema(schemaURL)
	if err != nil {
		return "", err
	}

	if err := writeFileAtomic(cachePath, data); err != nil {
		return "", err
	}
	return cachePath, nil
}

// writeFileAtomic writes data to a temporary file next to path and renames it
// into place, so a failed write never leaves a partial file at path.
func writeFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating cache file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing cache file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing cache file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("writing cache file: %w", err)
	}
	return nil
}

func hasSupported(fileMatch []string) bool {
	for _, fm := range fileMatch {
		base := fm
		if idx := strings.LastIndex(fm, "/"); idx >= 0 {
			base = fm[idx+1:]
		}
		ext := ""
		if idx := strings.LastIndex(base, "."); idx >= 0 {
			ext = base[idx+1:]
		}
		if _, ok := supportedExts[ext]; ok {
			return true
		}
		// Keep entries for extensionless files (e.g. ".babelrc", "Pipfile").
		// These are exact filename matches that SchemaStore explicitly provides
		// schemas for. An extensionless file is one with no extension after
		// stripping a leading dot (dotfiles like ".babelrc" have no extension).
		if ext == "" || ext == base || "."+ext == base {
			return true
		}
	}
	return false
}
