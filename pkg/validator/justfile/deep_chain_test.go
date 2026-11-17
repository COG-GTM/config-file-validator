package justfile

import (
	"bytes"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"testing"
)

// limitStack lowers the per-goroutine stack limit for the duration of a test
// so that any remaining unbounded recursion fails fast on modest inputs.
func limitStack(t *testing.T, bytes int) {
	t.Helper()
	old := debug.SetMaxStack(bytes)
	t.Cleanup(func() { debug.SetMaxStack(old) })
}

func operatorChain(op string, n int) []byte {
	var b bytes.Buffer
	b.Grow(len("x := x\n") + n*(len(op)+1))
	b.WriteString("x := x")
	for range n {
		b.WriteString(op)
		b.WriteString("x")
	}
	b.WriteString("\n")
	return b.Bytes()
}

func dependencyChain(n int) []byte {
	var b bytes.Buffer
	for i := range n - 1 {
		fmt.Fprintf(&b, "r%d: r%d\n", i, i+1)
	}
	fmt.Fprintf(&b, "r%d:\n", n-1)
	return b.Bytes()
}

func TestParseRejectsLongLogicalChains(t *testing.T) {
	limitStack(t, 8<<20)
	for _, op := range []string{"||", "&&"} {
		t.Run(op, func(t *testing.T) {
			_, err := Parse(operatorChain(op, 10_000_000))
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("expected ParseError, got %v", err)
			}
			if !strings.Contains(pe.Message, "nested too deeply") {
				t.Errorf("unexpected message: %q", pe.Message)
			}
		})
	}
}

func TestParseRejectsDeepNesting(t *testing.T) {
	n := maxExpressionDepth + 1
	cases := map[string]string{
		"parens":        "x := " + strings.Repeat("(", n) + "'a'" + strings.Repeat(")", n) + "\n",
		"concatenation": "x := 'a'" + strings.Repeat(" + 'a'", n) + "\n",
		"path_join":     "x := 'a'" + strings.Repeat(" / 'a'", n) + "\n",
		"else_if":       "x := " + strings.Repeat("if 'a' == 'b' { 'c' } else ", n) + "{ 'd' }\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(input))
			var pe *ParseError
			if !errors.As(err, &pe) || !strings.Contains(pe.Message, "nested too deeply") {
				t.Fatalf("expected depth ParseError, got %v", err)
			}
		})
	}
}

func TestParseAcceptsChainsWithinDepthLimit(t *testing.T) {
	inputs := [][]byte{
		operatorChain("||", maxExpressionDepth/2),
		operatorChain("&&", maxExpressionDepth/2),
		[]byte("x := 'a'" + strings.Repeat(" + 'a'", maxExpressionDepth/2) + "\n"),
	}
	for _, input := range inputs {
		jf, err := Parse(input)
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		if diags := jf.Validate(); len(diags) != 0 {
			t.Fatalf("unexpected diagnostics: %v", diags)
		}
	}
	// The depth counter must be released after each expression, so many
	// sibling expressions near the limit are still accepted.
	var b bytes.Buffer
	for i := range 50 {
		fmt.Fprintf(&b, "v%d := 'a'%s\n", i, strings.Repeat(" || 'a'", maxExpressionDepth/2))
	}
	if _, err := Parse(b.Bytes()); err != nil {
		t.Fatalf("Parse failed for sibling expressions: %v", err)
	}
}

func TestWalkExprDeepTreeDoesNotRecurse(t *testing.T) {
	limitStack(t, 1<<20)
	const depth = 1_000_000
	var expr Expression = &Variable{Name: "x"}
	for range depth {
		expr = &LogicalOp{Left: expr, Right: &Variable{Name: "x"}, Operator: "||"}
	}
	visited := 0
	walkExpr(expr, func(Expression) { visited++ })
	if want := 2*depth + 1; visited != want {
		t.Fatalf("visited %d nodes, want %d", visited, want)
	}
}

func TestWalkExprPreOrder(t *testing.T) {
	jf, err := Parse([]byte("x := f(a + b, if c == d { e } else { (g / h) }) || i && j\n"))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	var names []string
	walkExpr(jf.Assignments[0].Value, func(e Expression) {
		switch v := e.(type) {
		case *Variable:
			names = append(names, v.Name)
		case *FunctionCall:
			names = append(names, v.Name+"()")
		}
	})
	if got, want := strings.Join(names, ","), "f(),a,b,c,d,e,g,h,i,j"; got != want {
		t.Fatalf("walk order = %s, want %s", got, want)
	}
}

func TestValidateLongDependencyChain(t *testing.T) {
	limitStack(t, 8<<20)
	jf, err := Parse(dependencyChain(3_000_000))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if diags := jf.Validate(); len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags[0])
	}
}

func TestValidateLongDependencyChainWithCycle(t *testing.T) {
	limitStack(t, 8<<20)
	const n = 200_000
	input := append(dependencyChain(n), fmt.Sprintf("r%d: r0\n", n-1)...)
	jf, err := Parse(bytes.Replace(input, fmt.Appendf(nil, "r%d:\n", n-1), nil, 1))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	circular := 0
	for _, d := range jf.Validate() {
		if strings.Contains(d.Message, "circular dependency") {
			circular++
		}
	}
	if circular != n {
		t.Fatalf("got %d circular dependency diagnostics, want %d", circular, n)
	}
}

func TestValidateCircularDependencyReportsPathToCycle(t *testing.T) {
	jf, err := Parse([]byte("a: b\nb: c\nc: b\n"))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	got := map[string]bool{}
	for _, d := range jf.Validate() {
		if strings.Contains(d.Message, "circular dependency") {
			got[d.Message] = true
		}
	}
	for _, name := range []string{"b", "c"} {
		if !got[fmt.Sprintf("recipe '%s' has a circular dependency", name)] {
			t.Errorf("missing circular dependency diagnostic for %s: %v", name, got)
		}
	}
}
