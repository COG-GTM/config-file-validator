package justfile

import (
	"errors"
	"strings"
	"testing"
)

const hostileRepeat = 1_000_000

func deepNestingInputs() map[string][]byte {
	n := hostileRepeat
	return map[string][]byte{
		"parens":            []byte("x := " + strings.Repeat("(", n)),
		"concat_chain":      []byte("x := " + strings.Repeat(`"a" + `, n) + "\"a\"\n"),
		"path_join_chain":   []byte("x := " + strings.Repeat(`"a" / `, n) + "\"a\"\n"),
		"logical_or_chain":  []byte("x := " + strings.Repeat(`"a" || `, n) + "\"a\"\n"),
		"logical_and_chain": []byte("x := " + strings.Repeat(`"a" && `, n) + "\"a\"\n"),
		"nested_if":         []byte("x := " + strings.Repeat(`if "a" == "b" { `, n/10)),
		"else_if_chain":     []byte("x := " + strings.Repeat(`if "a" == "b" { "c" } else `, n/10) + "{ \"d\" }\n"),
		"nested_calls":      []byte("x := " + strings.Repeat("f(", n)),
		"interpolation":     []byte("r:\n    echo {{" + strings.Repeat("(", n) + "}}\n"),
		"param_default":     []byte("r p=" + strings.Repeat("(", n) + ":\n    echo\n"),
		"dependency_args":   []byte("r: (dep " + strings.Repeat("(", n) + ")\n    echo\n"),
	}
}

func TestParseRejectsDeeplyNestedExpressions(t *testing.T) {
	for name, input := range deepNestingInputs() {
		t.Run(name, func(t *testing.T) {
			jf, err := Parse(input)
			if err == nil {
				t.Fatalf("expected ParseError, got nil (jf=%v)", jf != nil)
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("expected *ParseError, got %T: %v", err, err)
			}
			if !strings.Contains(pe.Message, "nested too deeply") {
				t.Fatalf("expected nesting depth error, got %q", pe.Message)
			}
		})
	}
}

func TestParseAcceptsNestingWithinLimit(t *testing.T) {
	inputs := map[string]string{
		"parens":       "x := " + strings.Repeat("(", 200) + `"a"` + strings.Repeat(")", 200) + "\n",
		"concat_chain": "x := " + strings.Repeat(`"a" + `, 200) + "\"a\"\n",
		"or_chain":     "x := " + strings.Repeat(`"a" || `, 200) + "\"a\"\n",
		"else_if":      "x := " + strings.Repeat(`if "a" == "b" { "c" } else `, 200) + "{ \"d\" }\n",
		"nested_calls": "x := " + strings.Repeat("trim(", 200) + `"a"` + strings.Repeat(")", 200) + "\n",
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(input)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestParseRejectsOversizedInput(t *testing.T) {
	input := make([]byte, maxSourceSize+1)
	for i := range input {
		input[i] = '\n'
	}
	_, err := Parse(input)
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParseError, got %T: %v", err, err)
	}
	if !strings.Contains(pe.Message, "exceeds maximum size") {
		t.Fatalf("expected size error, got %q", pe.Message)
	}
}
