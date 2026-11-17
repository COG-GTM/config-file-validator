//go:build unix

package configfile

import (
	"fmt"
	"os"
	"syscall"
)

var currentUID = os.Geteuid

// untrustedReason reports why a discovered config file must not be used, or
// "" when it is safe. Files must be owned by the current user or root, must
// not be world-writable, and must not sit in a world-writable directory that
// lacks the sticky bit (where any user could replace them).
func untrustedReason(_ string, info os.FileInfo, dir string) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		uid := currentUID()
		if uid >= 0 && int64(st.Uid) != int64(uid) && st.Uid != 0 {
			return fmt.Sprintf("owned by uid %d, not the current user (uid %d)", st.Uid, uid)
		}
	}
	if info.Mode().Perm()&0o002 != 0 {
		return "file is writable by all users"
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return fmt.Sprintf("cannot inspect directory: %v", err)
	}
	if dirInfo.Mode().Perm()&0o002 != 0 && dirInfo.Mode()&os.ModeSticky == 0 {
		return "directory is writable by all users"
	}
	return ""
}
