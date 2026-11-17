package configfile

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/xeipuuv/gojsonschema"
)

//go:embed schema.json
var configSchema []byte

const FileName = ".cfv.toml"

// Config represents the parsed .cfv.toml configuration file.
type Config struct {
	ExcludeDirs      []string          `toml:"exclude-dirs"`
	ExcludeFileTypes []string          `toml:"exclude-file-types"`
	IgnoreFiles      []string          `toml:"ignore-files"`
	FileTypes        []string          `toml:"file-types"`
	Depth            *int              `toml:"depth"`
	Reporter         []string          `toml:"reporter"`
	GroupBy          []string          `toml:"groupby"`
	Quiet            *bool             `toml:"quiet"`
	RequireSchema    *bool             `toml:"require-schema"`
	NoSchema         *bool             `toml:"no-schema"`
	SchemaStore      *bool             `toml:"schemastore"`
	SchemaStorePath  *string           `toml:"schemastore-path"`
	Globbing         *bool             `toml:"globbing"`
	Gitignore        *bool             `toml:"gitignore"`
	SchemaMap        map[string]string `toml:"schema-map"`
	TypeMap          map[string]string `toml:"type-map"`
	Validators       ValidatorOptions  `toml:"validators"`
}

// ValidatorOptions holds per-validator configuration.
type ValidatorOptions struct {
	CSV  *CSVOptions  `toml:"csv"`
	JSON *JSONOptions `toml:"json"`
	INI  *INIOptions  `toml:"ini"`
}

// CSVOptions configures the CSV validator.
type CSVOptions struct {
	Delimiter  *string `toml:"delimiter"`
	Comment    *string `toml:"comment"`
	LazyQuotes *bool   `toml:"lazy-quotes"`
}

// JSONOptions configures the JSON validator.
type JSONOptions struct {
	ForbidDuplicateKeys *bool `toml:"forbid-duplicate-keys"`
}

// INIOptions configures the INI validator.
type INIOptions struct {
	ForbidDuplicateKeys *bool `toml:"forbid-duplicate-keys"`
}

// Load reads and validates a .cfv.toml file at the given path.
// It validates TOML syntax first, then validates against the embedded schema.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Validate TOML syntax
	var raw any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config file %s: invalid TOML syntax: %w", path, err)
	}

	// Convert to JSON for schema validation
	docJSON, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}

	// Validate against embedded schema
	result, err := gojsonschema.Validate(
		gojsonschema.NewBytesLoader(configSchema),
		gojsonschema.NewBytesLoader(docJSON),
	)
	if err != nil {
		return nil, fmt.Errorf("config file %s: schema validation error: %w", path, err)
	}
	if !result.Valid() {
		var errs []string
		for _, desc := range result.Errors() {
			errs = append(errs, desc.String())
		}
		return nil, fmt.Errorf("config file %s: schema validation failed: %s", path, joinErrors(errs))
	}

	// Parse into Config struct
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}

	return &cfg, nil
}

// Discover walks up from startDir looking for a trusted .cfv.toml file.
// Returns the path if found, or empty string if not found.
// See DiscoverWithWarnings for the search boundary and trust rules.
func Discover(startDir string) string {
	path, _ := DiscoverWithWarnings(startDir)
	return path
}

// DiscoverWithWarnings walks up from startDir looking for a .cfv.toml file.
//
// The search never climbs past the nearest ancestor that contains a .git
// entry or is the user's home directory. If neither exists above startDir,
// only startDir itself is checked, so a config planted in a shared parent
// directory such as /tmp is never picked up.
//
// A config file that is owned by another user (other than root), is
// writable by everyone, or sits in a directory that everyone can write to
// without the sticky bit is not used; a warning describing why is returned
// instead. The returned path is empty when no trusted config file is found.
func DiscoverWithWarnings(startDir string) (string, []string) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", nil
	}
	boundary := searchBoundary(dir, userHomeDir())
	for {
		path := filepath.Join(dir, FileName)
		if info, err := os.Stat(path); err == nil {
			if reason := untrustedReason(path, info, dir); reason != "" {
				return "", []string{fmt.Sprintf("ignoring config file %s: %s; pass --config to use it explicitly", path, reason)}
			}
			return path, nil
		}
		if dir == boundary {
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// searchBoundary returns the highest directory Discover may inspect: the
// nearest ancestor of start (inclusive) that contains .git or equals home.
// When there is no such ancestor, start itself is the boundary.
func searchBoundary(start, home string) string {
	dir := start
	for {
		if home != "" && dir == home {
			return dir
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	home, err = filepath.Abs(home)
	if err != nil || filepath.Dir(home) == home {
		// A home directory at the filesystem root would make the search unbounded.
		return ""
	}
	return home
}

func joinErrors(errs []string) string {
	if len(errs) == 1 {
		return errs[0]
	}
	result := errs[0]
	for _, e := range errs[1:] {
		result += "; " + e
	}
	return result
}
