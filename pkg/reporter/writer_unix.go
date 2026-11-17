//go:build unix

package reporter

import "syscall"

// openNoFollow makes the final open fail if the report path was swapped for a
// symbolic link after the Lstat check.
const openNoFollow = syscall.O_NOFOLLOW
