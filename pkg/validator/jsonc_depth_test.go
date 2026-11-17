package validator

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func Test_JSONCRejectsDeepNesting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   []byte
		wantCol int
	}{
		{"arrays", []byte(strings.Repeat("[", 10_000_000)), maxJSONCDepth + 1},
		{"objects", []byte(strings.Repeat(`{"a":`, 2_000_000)), 5*maxJSONCDepth + 1},
	}
	for _, tc := range tests {
		input := tc.input
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()

			valid, err := JSONCValidator{}.ValidateSyntax(input)
			if valid || err == nil {
				t.Fatalf("expected deep nesting to be rejected, got valid=%v err=%v", valid, err)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Line != 1 || ve.Column != tc.wantCol {
				t.Fatalf("expected ValidationError at line 1, column %d, got %#v", tc.wantCol, err)
			}
			if !strings.Contains(err.Error(), "exceeded max depth") {
				t.Fatalf("unexpected error: %v", err)
			}

			if _, err := (JSONCValidator{}).MarshalToJSON(input); err == nil {
				t.Fatal("MarshalToJSON: expected error")
			}
			if valid, err := (JSONCValidator{}).ValidateSchema(input, ""); valid || err == nil {
				t.Fatalf("ValidateSchema: expected error, got valid=%v err=%v", valid, err)
			}

			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Fatalf("rejection took %s", elapsed)
			}
		})
	}
}

func Test_JSONCNestingAtLimit(t *testing.T) {
	t.Parallel()

	atLimit := strings.Repeat("[", maxJSONCDepth) + strings.Repeat("]", maxJSONCDepth)
	if valid, err := (JSONCValidator{}).ValidateSyntax([]byte(atLimit)); !valid || err != nil {
		t.Fatalf("nesting at the limit should be valid, got valid=%v err=%v", valid, err)
	}

	overLimit := "[" + atLimit + "]"
	if valid, _ := (JSONCValidator{}).ValidateSyntax([]byte(overLimit)); valid {
		t.Fatal("nesting one past the limit should be rejected")
	}
}

func Test_JSONCDepthIgnoresStringsAndComments(t *testing.T) {
	t.Parallel()

	brackets := strings.Repeat("[{", maxJSONCDepth)
	inputs := map[string]string{
		"string":         `{"a": "` + brackets + `", "b": "\"` + brackets + `"}`,
		"line comment":   "// " + brackets + "\n{\"a\": 1}",
		"block comment":  "/* " + brackets + " */ {\"a\": 1}",
		"unterminated":   `["` + brackets,
		"trailing slash": `{"a": 1}/`,
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := checkJSONCDepth([]byte(input))
			if err != nil {
				t.Fatalf("unexpected depth error: %v", err)
			}
		})
	}

	valid, err := JSONCValidator{}.ValidateSyntax([]byte(inputs["string"]))
	if !valid || err != nil {
		t.Fatalf("brackets inside strings should not count, got valid=%v err=%v", valid, err)
	}
}
