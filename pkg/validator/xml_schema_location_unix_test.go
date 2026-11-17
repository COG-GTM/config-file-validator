//go:build unix

package validator

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A FIFO blocks any reader until a writer appears, so a resolver that opens
// the target hangs here. Rejected locations must not be opened at all.
func Test_XMLSchemaLocationDoesNotOpenRejectedTargets(t *testing.T) {
	t.Parallel()
	base, root := schemaLocationFixture(t)
	fifo := filepath.Join(base, "pipe.xsd")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))

	for _, loc := range []string{fifo, "../pipe.xsd"} {
		done := make(chan error, 1)
		go func() {
			_, err := XMLValidator{}.ValidateSchema(xmlWithSchemaLocation(loc), filepath.Join(root, "doc.xml"))
			done <- err
		}()
		select {
		case err := <-done:
			require.Error(t, err, loc)
		case <-time.After(5 * time.Second):
			t.Fatalf("validator opened schema location %q outside the root", loc)
		}
	}
}
