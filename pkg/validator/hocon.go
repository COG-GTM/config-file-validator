package validator

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/scanner"
	"unicode"

	"github.com/gurkankaymak/hocon"
)

var hoconPosRe = regexp.MustCompile(`(.*?)\s*at: (\d+):(\d+),?\s*(.*)`)

// ErrHoconInclude is returned when a HOCON document contains an include
// directive. Includes make the parser open arbitrary local files, so they are
// rejected before parsing.
var ErrHoconInclude = errors.New("include directives are not supported")

// HoconValidator is used to validate a byte slice that is intended to represent a
// HOCON file.
type HoconValidator struct{}

var _ Validator = HoconValidator{}

// hoconSubstitution is a ${path} or ${?path} reference found in a document.
type hoconSubstitution struct {
	path     string
	optional bool
	line     int
	column   int
}

// ValidateSyntax checks if the provided byte slice represents a valid .hocon file.
// Include directives are rejected and substitutions are resolved only against
// the document itself, never against the process environment.
func (HoconValidator) ValidateSyntax(b []byte) (bool, error) {
	subs, err := scanHocon(string(b))
	if err != nil {
		return false, err
	}

	config, err := hocon.ParseStringUnresolved(string(b))
	if err != nil {
		return false, hoconError(err)
	}

	if err := resolveHoconWithoutEnv(config, subs); err != nil {
		return false, err
	}

	return true, nil
}

// resolveHoconWithoutEnv resolves substitutions only when every referenced path
// is defined in the document, because the library falls back to environment
// variables for undefined paths.
func resolveHoconWithoutEnv(config *hocon.Config, subs []hoconSubstitution) error {
	if _, ok := config.GetRoot().(hocon.Object); !ok {
		return nil
	}

	undefined := false
	for _, s := range subs {
		if config.HasPath(s.path) {
			continue
		}
		if !s.optional {
			return &ValidationError{
				Err:    fmt.Errorf("could not resolve substitution: ${%s} to a value", s.path),
				Line:   s.line,
				Column: s.column,
			}
		}
		undefined = true
	}

	if undefined {
		return nil
	}

	if _, err := config.Resolve(); err != nil {
		return hoconError(err)
	}

	return nil
}

func hoconError(err error) error {
	if m := hoconPosRe.FindStringSubmatch(err.Error()); m != nil {
		line, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		msg := strings.TrimSpace(m[1])
		if m[4] != "" {
			if msg != "" {
				msg += ": "
			}
			msg += m[4]
		}
		return &ValidationError{Err: errors.New(msg), Line: line, Column: col}
	}
	return err
}

// newHoconScanner mirrors the tokenizer configuration used by
// github.com/gurkankaymak/hocon so tokens line up with the parser's view.
func newHoconScanner(src string) *scanner.Scanner {
	s := new(scanner.Scanner)
	s.Init(strings.NewReader(src))
	s.Whitespace ^= 1<<'\t' | 1<<' '
	s.Mode &^= scanner.ScanComments | scanner.SkipComments
	s.Error = func(*scanner.Scanner, string) {}
	s.IsIdentRune = func(ch rune, i int) bool {
		return ch == '_' || ch == '-' || unicode.IsLetter(ch) || unicode.IsDigit(ch) && i > 0
	}
	return s
}

// scanHocon walks the document's tokens, skipping comments and string
// literals. It returns an error for any include directive and collects the
// substitutions it encounters.
func scanHocon(src string) ([]hoconSubstitution, error) {
	s := newHoconScanner(src)
	var subs []hoconSubstitution

	tok := s.Scan()
	for tok != scanner.EOF {
		text := s.TokenText()
		switch {
		case text == "#" || (text == "/" && s.Peek() == '/'):
			for tok != scanner.EOF && tok != '\n' {
				tok = s.Scan()
			}
			continue
		case text == `""` && s.Peek() == '"':
			skipHoconMultiLineString(s)
		case tok == scanner.Ident && text == "include":
			line, col := s.Line, s.Column
			tok = scanSkipBlanks(s)
			if tok == scanner.String || (tok == scanner.Ident && isHoconIncludeQualifier(s.TokenText())) {
				return nil, &ValidationError{Err: ErrHoconInclude, Line: line, Column: col}
			}
			continue
		case text == "$" && s.Peek() == '{':
			sub := hoconSubstitution{line: s.Line, column: s.Column}
			s.Scan()
			tok = scanSkipBlanks(s)
			if s.TokenText() == "?" {
				sub.optional = true
				tok = scanSkipBlanks(s)
			}
			var path strings.Builder
			for tok != scanner.EOF && tok != '\n' && s.TokenText() != "}" {
				path.WriteString(s.TokenText())
				tok = scanSkipBlanks(s)
			}
			sub.path = path.String()
			subs = append(subs, sub)
		default:
		}
		tok = s.Scan()
	}

	return subs, nil
}

func scanSkipBlanks(s *scanner.Scanner) rune {
	tok := s.Scan()
	for tok == ' ' || tok == '\t' {
		tok = s.Scan()
	}
	return tok
}

func isHoconIncludeQualifier(text string) bool {
	return text == "required" || text == "file" || text == "classpath"
}

// skipHoconMultiLineString consumes the remainder of a """...""" string whose
// opening "" has already been scanned as an empty string token.
func skipHoconMultiLineString(s *scanner.Scanner) {
	s.Next()
	quotes := 0
	for r := s.Next(); r != scanner.EOF; r = s.Next() {
		if r == '"' {
			quotes++
		} else {
			quotes = 0
		}
		if quotes >= 3 && s.Peek() != '"' {
			return
		}
	}
}
