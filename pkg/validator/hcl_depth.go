package validator

import (
	"bytes"
	"unicode"
	"unicode/utf8"
)

const (
	// maxHclNestingDepth bounds how deeply brackets, template interpolations,
	// and template directives may nest before HclValidator rejects the input.
	// The HCL parser is recursive descent with no limit of its own, so
	// unbounded nesting can exhaust the goroutine stack and abort the process.
	maxHclNestingDepth = 1000

	// maxHclFileSize is the largest input HclValidator will hand to the parser.
	maxHclFileSize = 16 << 20

	hclNoLimitExceeded = -1
)

const (
	hclFrameBrack   = '['
	hclFrameBrace   = '{'
	hclFrameParen   = '('
	hclFrameInterp  = 'i'
	hclFrameQuoted  = '"'
	hclFrameHeredoc = 'h'
	hclFrameIfDir   = 'c'
	hclFrameForDir  = 'f'
)

type hclFrame struct {
	kind  byte
	delim []byte
}

// hclDepthScanner is a single-pass, non-recursive model of the HCL native
// syntax lexer (hclsyntax/scan_tokens.rl). It tracks only what affects parser
// nesting: brackets, quoted and heredoc templates, interpolation and directive
// sequences, and comments. Where it cannot match a closer to its opener it
// keeps the opener, so malformed input can only overestimate depth.
type hclDepthScanner struct {
	b     []byte
	i     int
	limit int
	stack []hclFrame
}

// hclNestingExceeded returns the byte offset at which nesting in b first
// exceeds limit, or hclNoLimitExceeded if it never does.
func hclNestingExceeded(b []byte, limit int) int {
	s := &hclDepthScanner{b: b, limit: limit}
	for s.i < len(s.b) {
		start := s.i
		var exceeded bool
		if t := s.templateFrame(); t < 0 {
			exceeded = s.stepExpression()
		} else {
			exceeded = s.stepTemplate(t)
		}
		if exceeded {
			return start
		}
	}
	return hclNoLimitExceeded
}

// templateFrame returns the index of the template frame that encloses the
// current position, or -1 when the scanner is in expression context.
func (s *hclDepthScanner) templateFrame() int {
	for j := len(s.stack) - 1; j >= 0; j-- {
		switch s.stack[j].kind {
		case hclFrameIfDir, hclFrameForDir:
			continue
		case hclFrameQuoted, hclFrameHeredoc:
			return j
		default:
			return -1
		}
	}
	return -1
}

func (s *hclDepthScanner) top() byte {
	if len(s.stack) == 0 {
		return 0
	}
	return s.stack[len(s.stack)-1].kind
}

func (s *hclDepthScanner) push(f hclFrame) bool {
	s.stack = append(s.stack, f)
	return len(s.stack) > s.limit
}

func (s *hclDepthScanner) peek(n int) byte {
	if s.i+n < len(s.b) {
		return s.b[s.i+n]
	}
	return 0
}

// skipComment skips a comment starting at j and returns the offset after it,
// or j if there is no comment there. An unterminated /* is not a comment.
func (s *hclDepthScanner) skipComment(j int) int {
	rest := s.b[j:]
	switch {
	case bytes.HasPrefix(rest, []byte("#")), bytes.HasPrefix(rest, []byte("//")):
		if k := bytes.IndexByte(rest, '\n'); k >= 0 {
			return j + k
		}
		return len(s.b)
	case bytes.HasPrefix(rest, []byte("/*")):
		if k := bytes.Index(rest[2:], []byte("*/")); k >= 0 {
			return j + 2 + k + 2
		}
	}
	return j
}

func (s *hclDepthScanner) stepExpression() bool {
	if next := s.skipComment(s.i); next != s.i {
		s.i = next
		return false
	}

	c := s.b[s.i]
	switch {
	case c == '"':
		s.i++
		return s.push(hclFrame{kind: hclFrameQuoted})
	case c == '<' && s.peek(1) == '<':
		if delim, next, ok := s.heredocStart(); ok {
			s.i = next
			return s.push(hclFrame{kind: hclFrameHeredoc, delim: delim})
		}
		s.i++
	case c == '[', c == '{', c == '(':
		s.i++
		return s.push(hclFrame{kind: c})
	case c == ']', c == ')':
		s.i++
		if (c == ']' && s.top() == hclFrameBrack) || (c == ')' && s.top() == hclFrameParen) {
			s.stack = s.stack[:len(s.stack)-1]
		}
	case c == '}':
		s.i++
		s.closeBrace()
	default:
		s.i++
	}
	return false
}

// closeBrace mirrors the lexer, which tracks only braces: a "}" closes the
// innermost "{" or interpolation, implicitly closing any brackets inside it.
func (s *hclDepthScanner) closeBrace() {
	for j := len(s.stack) - 1; j >= 0; j-- {
		switch s.stack[j].kind {
		case hclFrameBrack, hclFrameParen:
			continue
		case hclFrameBrace, hclFrameInterp:
			s.stack = s.stack[:j]
		}
		return
	}
}

func isHclIdentStart(r rune) bool {
	return r == '_' || unicode.In(r, unicode.L, unicode.Nl, unicode.Other_ID_Start)
}

func isHclIdentContinue(r rune) bool {
	return r == '-' || isHclIdentStart(r) ||
		unicode.In(r, unicode.Mn, unicode.Mc, unicode.Nd, unicode.Pc, unicode.Other_ID_Continue)
}

// heredocStart recognizes "<<ID\n" or "<<-ID\n" at the current position and
// returns the delimiter and the offset of the first body byte.
func (s *hclDepthScanner) heredocStart() ([]byte, int, bool) {
	j := s.i + 2
	if j < len(s.b) && s.b[j] == '-' {
		j++
	}
	start := j
	for j < len(s.b) {
		r, size := utf8.DecodeRune(s.b[j:])
		if r == utf8.RuneError || !(isHclIdentContinue(r) && (j > start || isHclIdentStart(r))) {
			break
		}
		j += size
	}
	if j == start {
		return nil, 0, false
	}
	delim := s.b[start:j]
	if j < len(s.b) && s.b[j] == '\r' {
		j++
	}
	if j >= len(s.b) || s.b[j] != '\n' {
		return nil, 0, false
	}
	return delim, j + 1, true
}

// heredocEnd reports whether the line starting at the current position is the
// heredoc's closing marker, and returns the offset of its newline.
func (s *hclDepthScanner) heredocEnd(delim []byte) (int, bool) {
	k := bytes.IndexByte(s.b[s.i:], '\n')
	if k < 0 || !bytes.Equal(bytes.TrimSpace(s.b[s.i:s.i+k]), delim) {
		return 0, false
	}
	return s.i + k, true
}

func (s *hclDepthScanner) stepTemplate(t int) bool {
	quoted := s.stack[t].kind == hclFrameQuoted
	if !quoted && (s.i == 0 || s.b[s.i-1] == '\n') {
		if next, ok := s.heredocEnd(s.stack[t].delim); ok {
			s.stack = s.stack[:t]
			s.i = next
			return false
		}
	}

	c := s.b[s.i]
	switch {
	case quoted && c == '\\':
		s.i += 2
	case quoted && c == '"':
		s.stack = s.stack[:t]
		s.i++
	case (c == '$' || c == '%') && s.peek(1) == c && s.peek(2) == '{':
		s.i += 3
	case c == '$' && s.peek(1) == '{':
		s.i += 2
		return s.push(hclFrame{kind: hclFrameInterp})
	case c == '%' && s.peek(1) == '{':
		s.i += 2
		return s.directive()
	default:
		s.i++
	}
	return false
}

// directive handles the start of a %{ sequence, tracking if/for nesting.
func (s *hclDepthScanner) directive() bool {
	j := s.i
	if j < len(s.b) && s.b[j] == '~' {
		j++
	}
	for j < len(s.b) {
		if next := s.skipComment(j); next != j {
			j = next
			continue
		}
		if c := s.b[j]; c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			break
		}
		j++
	}
	k := j
	for k < len(s.b) {
		c := s.b[k]
		if c != '_' && c != '-' && c < 0x80 && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			break
		}
		k++
	}

	switch string(s.b[j:k]) {
	case "if":
		if s.push(hclFrame{kind: hclFrameIfDir}) {
			return true
		}
	case "for":
		if s.push(hclFrame{kind: hclFrameForDir}) {
			return true
		}
	case "endif":
		if s.top() == hclFrameIfDir {
			s.stack = s.stack[:len(s.stack)-1]
		}
	case "endfor":
		if s.top() == hclFrameForDir {
			s.stack = s.stack[:len(s.stack)-1]
		}
	}
	return s.push(hclFrame{kind: hclFrameInterp})
}
