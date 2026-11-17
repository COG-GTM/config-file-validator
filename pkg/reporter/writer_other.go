//go:build !unix

package reporter

// openNoFollow is zero on platforms without O_NOFOLLOW; the Lstat check in
// outputBytesToFile still rejects symbolic links there.
const openNoFollow = 0
