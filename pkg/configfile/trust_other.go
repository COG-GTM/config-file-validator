//go:build !unix

package configfile

import "os"

// untrustedReason has no ownership model to check on non-Unix platforms;
// discovery there relies on the search boundary alone.
func untrustedReason(_ string, _ os.FileInfo, _ string) string {
	return ""
}
