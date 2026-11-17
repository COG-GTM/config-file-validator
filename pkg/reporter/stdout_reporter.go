package reporter

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/fatih/color"
)

type StdoutReporter struct {
	outputDest string
}

type reportStdout struct {
	Text    string
	Summary summary
}

func NewStdoutReporter(outputDest string) *StdoutReporter {
	return &StdoutReporter{
		outputDest: outputDest,
	}
}

// Print implements the Reporter interface by outputting
// the report content to stdout
func (sr StdoutReporter) Print(reports []Report) error {
	stdoutReport := createStdoutReport(reports, 1)

	if sr.outputDest != "" {
		return outputBytesToFile(sr.outputDest, "result", "txt", []byte(stdoutReport.Text))
	}

	if len(reports) > 0 && !reports[0].IsQuiet {
		fmt.Print(stdoutReport.Text)
	}

	return nil
}

// PrintGroupStdout prints a recursive grouped report to stdout.
func PrintGroupStdout(groupReport *GroupNode) error {
	totalSummary := printGroupNodeStdout(groupReport, nil, 0)
	fmt.Printf("Total Summary: %d succeeded, %d failed\n", totalSummary.Passed, totalSummary.Failed)
	return nil
}

func printGroupNodeStdout(node *GroupNode, groupPath []string, depth int) summary {
	totalSummary := summary{}

	for _, child := range node.Children {
		fmt.Printf("%s%s\n", strings.Repeat("    ", depth), guardLineStart(escapeControlChars(child.Key)))
		childSummary := printGroupNodeStdout(child, append(slices.Clone(groupPath), child.Key), depth+1)
		totalSummary.Passed += childSummary.Passed
		totalSummary.Failed += childSummary.Failed
	}

	if len(node.Children) > 0 {
		return totalSummary
	}

	stdoutReport := createStdoutReport(node.Reports, depth)
	totalSummary.Passed += stdoutReport.Summary.Passed
	totalSummary.Failed += stdoutReport.Summary.Failed
	fmt.Println(stdoutReport.Text)
	if len(groupPath) > 0 && checkGroupsForPassFail(groupPath...) {
		summaryDepth := depth - 1
		if summaryDepth < 0 {
			summaryDepth = 0
		}
		fmt.Printf(
			"%sSummary: %d succeeded, %d failed\n\n",
			strings.Repeat("    ", summaryDepth),
			stdoutReport.Summary.Passed,
			stdoutReport.Summary.Failed,
		)
	}

	return totalSummary
}

// PrintSingleGroupStdout prints a grouped report with one grouping level.
func PrintSingleGroupStdout(groupReport map[string][]Report) error {
	return PrintGroupStdout(groupNodeFromSingle(groupReport))
}

// PrintDoubleGroupStdout prints a grouped report with two grouping levels.
func PrintDoubleGroupStdout(groupReport map[string]map[string][]Report) error {
	return PrintGroupStdout(groupNodeFromDouble(groupReport))
}

// PrintTripleGroupStdout prints a grouped report with three grouping levels.
func PrintTripleGroupStdout(groupReport map[string]map[string]map[string][]Report) error {
	return PrintGroupStdout(groupNodeFromTriple(groupReport))
}

// Checks if any of the provided groups are "Passed" or "Failed".
func checkGroupsForPassFail(groups ...string) bool {
	for _, group := range groups {
		if group == "Passed" || group == "Failed" {
			return false
		}
	}
	return true
}

// Creates the standard text report
func createStdoutReport(reports []Report, indentSize int) reportStdout {
	result := reportStdout{}
	baseIndent := "    "
	indent, errIndent := strings.Repeat(baseIndent, indentSize), strings.Repeat(baseIndent, indentSize+1)

	for _, report := range reports {
		if !report.IsValid {
			fmtRed := color.New(color.FgRed)
			result.Text += fmtRed.Sprintf("%s× %s\n", indent, escapeControlChars(report.FilePath))
			for _, e := range report.ValidationErrors {
				paddedString := padErrorString(e)
				result.Text += fmtRed.Sprintf("%serror: %v\n", errIndent, paddedString)
			}
			for _, n := range report.Notes {
				paddedString := padErrorString(n)
				result.Text += color.New(color.FgYellow).Sprintf("%snote: %v\n", errIndent, paddedString)
			}
			result.Summary.Failed++
		} else {
			result.Text += color.New(color.FgGreen).Sprintf("%s✓ %s\n", indent, escapeControlChars(report.FilePath))
			result.Summary.Passed++
		}
		for _, w := range report.Warnings {
			paddedString := padErrorString(w)
			result.Text += color.New(color.FgYellow).Sprintf("%swarning: %v\n", errIndent, paddedString)
		}
	}

	return result
}

// padErrorString adds padding to every newline in the error
// string, except the first line and removes any trailing newlines
// or spaces. Control characters in each line are escaped and
// continuation lines are guarded against CI log command prefixes.
func padErrorString(errS string) string {
	errS = strings.TrimSpace(errS)
	lines := strings.Split(errS, "\n")
	for idx := range lines {
		line := escapeControlChars(strings.TrimSuffix(lines[idx], "\r"))
		if idx > 0 {
			line = "               " + guardLineStart(line)
		}
		lines[idx] = line
	}
	paddedErr := strings.Join(lines, "\n")
	return paddedErr
}

// escapeControlChars replaces control characters and Unicode line or
// paragraph separators with a visible Go escape sequence so untrusted
// text cannot start new output lines or emit terminal escape codes.
func escapeControlChars(s string) string {
	if !strings.ContainsFunc(s, isUnsafeRune) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if isUnsafeRune(r) {
			quoted := strconv.QuoteRuneToASCII(r)
			b.WriteString(quoted[1 : len(quoted)-1])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isUnsafeRune(r rune) bool {
	return (unicode.IsControl(r) && r != '\t') || r == '\u2028' || r == '\u2029'
}

// guardLineStart escapes the first character of a line that would
// otherwise be read as a GitHub Actions ("::") or Azure Pipelines
// ("##[") log command once leading whitespace is trimmed.
func guardLineStart(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, "::") && !strings.HasPrefix(trimmed, "##[") {
		return line
	}
	lead := line[:len(line)-len(trimmed)]
	return lead + fmt.Sprintf("\\x%02x", trimmed[0]) + trimmed[1:]
}
