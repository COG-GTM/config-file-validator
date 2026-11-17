package validator

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/hcl/v2/hclparse"
)

// HclValidator is used to validate a byte slice that is intended to represent a
// HashiCorp Configuration Language (HCL) file.
type HclValidator struct{}

var _ Validator = HclValidator{}

// Validate checks if the provided byte slice represents a valid .hcl file.
//
// The hcl parser uses FIFO to determine which error to display to the user. For
// more information, see the documentation at:
//
// https://pkg.go.dev/github.com/hashicorp/hcl/v2#Diagnostics.Error
//
// If the hcl.Diagnostics slice contains more than one error, the wrapped
// error returned by this function will include them as "and {count} other
// diagnostic(s)" in the error message.
//
// Inputs larger than 16 MiB, or whose brackets, template interpolations, or
// template directives nest more than 1000 levels deep, are rejected before
// parsing so that hostile input cannot exhaust the parser's stack.
func (HclValidator) ValidateSyntax(b []byte) (bool, error) {
	if len(b) > maxHclFileSize {
		return false, &ValidationError{
			Err: fmt.Errorf("file size %d bytes exceeds the %d byte limit for HCL files", len(b), maxHclFileSize),
		}
	}
	if off := hclNestingExceeded(b, maxHclNestingDepth); off != hclNoLimitExceeded {
		line, col := hclPosition(b, off)
		return false, &ValidationError{
			Err:    fmt.Errorf("nesting depth exceeds the maximum of %d", maxHclNestingDepth),
			Line:   line,
			Column: col,
		}
	}

	_, diags := hclparse.NewParser().ParseHCL(b, "")
	if diags == nil {
		return true, nil
	}

	var row, col int
	if subject := diags[0].Subject; subject != nil {
		row = subject.Start.Line
		col = subject.Start.Column
	}

	msg := diags[0].Summary
	if diags[0].Detail != "" {
		msg += ": " + diags[0].Detail
	}
	if len(diags) > 1 {
		msg += fmt.Sprintf(" (and %d other diagnostic(s))", len(diags)-1)
	}

	return false, &ValidationError{
		Err:    fmt.Errorf("%s", strings.TrimSpace(msg)),
		Line:   row,
		Column: col,
	}
}

// hclPosition converts a byte offset in b to a 1-based line and column.
func hclPosition(b []byte, off int) (int, int) {
	prefix := b[:off]
	lineStart := bytes.LastIndexByte(prefix, '\n') + 1
	return bytes.Count(prefix, []byte{'\n'}) + 1, utf8.RuneCount(prefix[lineStart:]) + 1
}
