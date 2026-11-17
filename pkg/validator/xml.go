package validator

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/lestrrat-go/helium"
	"github.com/lestrrat-go/helium/xsd"
)

var (
	xmlLineColRe     = regexp.MustCompile(`at line (\d+), column (\d+)`)
	xmlStripPosition = regexp.MustCompile(`\s*at line \d+, column \d+`)
	xsdErrorLineRe   = regexp.MustCompile(`^\(string\):(\d+): Schemas validity error : (.+)`)
)

type XMLValidator struct{}

var _ Validator = XMLValidator{}

// ValidateXSD satisfies the XMLSchemaValidator marker interface.
func (XMLValidator) ValidateXSD(b []byte, schemaPath string) (bool, error) {
	return ValidateXSD(b, schemaPath)
}

// hasDOCTYPE reports whether b contains an XML DOCTYPE declaration.
// It walks tokens rather than doing a plain substring scan, so a
// "<!DOCTYPE" that only appears inside a comment does not count as a
// real declaration. The XML spec requires a DOCTYPE to appear before
// the root element, so scanning stops as soon as the root start
// element is reached.
func hasDOCTYPE(b []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(b))
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if directive, ok := token.(xml.Directive); ok && bytes.HasPrefix(bytes.TrimSpace(directive), []byte("DOCTYPE")) {
			return true
		}
		if _, ok := token.(xml.StartElement); ok {
			return false
		}
	}
}

func (XMLValidator) ValidateSyntax(b []byte) (bool, error) {
	ctx := context.Background()
	parser := helium.NewParser()
	if hasDOCTYPE(b) {
		parser = parser.ValidateDTD(true)
	}
	_, err := parser.Parse(ctx, b)
	if err != nil {
		errMsg := err.Error()
		if m := xmlLineColRe.FindStringSubmatch(errMsg); m != nil {
			line, _ := strconv.Atoi(m[1])
			col, _ := strconv.Atoi(m[2])
			cleanMsg := xmlStripPosition.ReplaceAllString(errMsg, "")
			return false, &ValidationError{Err: fmt.Errorf("%s", strings.TrimSpace(cleanMsg)), Line: line, Column: col}
		}
		return false, err
	}
	return true, nil
}

func (XMLValidator) ValidateSchema(b []byte, filePath string) (bool, error) {
	schemaLoc, err := extractXSDLocation(b)
	if err != nil {
		return false, err
	}
	if schemaLoc == "" {
		return true, ErrNoSchema
	}

	schemaPath, err := resolveXSDPath(schemaLoc, filePath)
	if err != nil {
		return false, err
	}
	return ValidateXSD(b, schemaPath)
}

// ValidateXSD validates XML bytes against an XSD file at the given path.
// Exported for use by the CLI when applying external schemas.
func ValidateXSD(b []byte, schemaPath string) (bool, error) {
	ctx := context.Background()
	parser := helium.NewParser()

	schema, err := xsd.NewCompiler().CompileFile(ctx, schemaPath)
	if err != nil {
		return false, fmt.Errorf("schema compilation error: %w", err)
	}

	doc, err := parser.Parse(ctx, b)
	if err != nil {
		return false, fmt.Errorf("xml parse error: %w", err)
	}

	ec := helium.NewErrorCollector(ctx, helium.ErrorLevelNone)
	if err := xsd.NewValidator(schema).ErrorHandler(ec).Validate(ctx, doc); err != nil {
		var msgs []string
		for _, e := range ec.Errors() {
			msgs = append(msgs, cleanXSDError(e.Error()))
		}
		if len(msgs) > 0 {
			return false, &SchemaErrors{Prefix: "schema validation failed: ", Items: msgs}
		}
		return false, fmt.Errorf("schema validation failed: %w", err)
	}

	return true, nil
}

const xsiNamespace = "http://www.w3.org/2001/XMLSchema-instance" //nolint:revive // XSI namespace is a fixed URI; DevSkim: ignore DS137138

func extractXSDLocation(b []byte) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(b)))
	for {
		tok, err := decoder.Token()
		if err != nil {
			return "", nil
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local != "noNamespaceSchemaLocation" {
				continue
			}
			if attr.Name.Space == xsiNamespace {
				return strings.TrimSpace(attr.Value), nil
			}
			return "", fmt.Errorf(
				"noNamespaceSchemaLocation uses incorrect namespace %q, expected %q",
				attr.Name.Space, xsiNamespace,
			)
		}
		return "", nil
	}
}

// maxXSDSize caps the size of a schema file referenced from a document.
const maxXSDSize = 10 << 20

// resolveXSDPath maps an xsi:noNamespaceSchemaLocation value taken from an
// untrusted document to a schema file on disk. The value must be a relative
// path to a regular ".xsd" file that, after cleaning and resolving symlinks,
// stays inside the working directory or the document's own directory.
// Candidates outside those roots are rejected before they are touched.
func resolveXSDPath(schemaLoc, filePath string) (string, error) {
	if filepath.IsAbs(schemaLoc) || filepath.VolumeName(schemaLoc) != "" ||
		strings.HasPrefix(schemaLoc, "/") || strings.HasPrefix(schemaLoc, `\`) ||
		strings.Contains(schemaLoc, "://") {
		return "", fmt.Errorf("schema location %q must be a relative path", schemaLoc)
	}
	if !strings.EqualFold(filepath.Ext(schemaLoc), ".xsd") {
		return "", fmt.Errorf("schema location %q must reference a .xsd file", schemaLoc)
	}

	roots := xsdRoots(filePath)
	candidates := []string{schemaLoc, filepath.Join(filepath.Dir(filePath), schemaLoc)}
	for _, candidate := range candidates {
		abs, err := filepath.Abs(candidate)
		if err != nil || !withinAny(abs, roots.abs) {
			continue
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil || !withinAny(real, roots.real) ||
			!strings.EqualFold(filepath.Ext(real), ".xsd") {
			continue
		}
		info, err := os.Stat(real)
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("schema location %q is not a regular file", schemaLoc)
		}
		if info.Size() > maxXSDSize {
			return "", fmt.Errorf("schema location %q exceeds the %d byte size limit", schemaLoc, maxXSDSize)
		}
		return real, nil
	}
	return "", fmt.Errorf("%w: %q must name a .xsd file inside the working directory or the XML file's directory",
		errXSDNotFound, schemaLoc)
}

var errXSDNotFound = errors.New("schema location not found")

type xsdRootSet struct {
	abs  []string
	real []string
}

// xsdRoots returns the directories a document-declared schema may live in:
// the working directory and the directory of the XML file.
func xsdRoots(filePath string) xsdRootSet {
	var set xsdRootSet
	dirs := []string{"."}
	if filePath != "" {
		dirs = append(dirs, filepath.Dir(filePath))
	}
	for _, dir := range dirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		set.abs = append(set.abs, abs)
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			set.real = append(set.real, real)
		}
	}
	return set
}

func withinAny(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// cleanXSDError reformats helium XSD errors from
// "(string):5: Schemas validity error : Element 'port': ...\n"
// to "line 5: Element 'port': ..."
func cleanXSDError(s string) string {
	s = strings.TrimSpace(s)
	if m := xsdErrorLineRe.FindStringSubmatch(s); m != nil {
		return fmt.Sprintf("line %s: %s", m[1], m[2])
	}
	return s
}
