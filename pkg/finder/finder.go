package finder

import (
	"errors"

	"github.com/Boeing/config-file-validator/v2/pkg/filetype"
)

// ErrSymlinkNotFollowed is set as FileMetadata.ReadError for a symlink whose
// target is missing or resolves outside the search path. Both cases share one
// error so the report does not reveal whether an outside path exists.
var ErrSymlinkNotFollowed = errors.New("symlink not followed: target is missing or outside the search path")

// The File Metadata object stores the
// name and the path of the file and the type
// of file that it is, example: json, yml, etc
type FileMetadata struct {
	Name     string
	Path     string
	FileType filetype.FileType
	// ReadError is non-nil when the file was matched but must not be read,
	// for example ErrSymlinkNotFollowed.
	ReadError error
}

// FileFinder is the interface that wraps the Find method

// Find will return an array of FileMetadata objects from
// a provided path and array of FileTypes. Any files in
// subdirectories defined in excludeDirs will not be returned
type FileFinder interface {
	Find() ([]FileMetadata, error)
}
