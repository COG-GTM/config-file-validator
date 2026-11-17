package reporter

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// outputBytesToFile outputs the named file at the destination specified by outputDest.
// if an existing directory is provided to outputDest param, creates a file named with defaultName given at the directory.
// if outputDest specifies a path to the file, creates the file named with outputDest.
// when empty string is given to outputDest param, it returns error.
// it refuses to write through a symbolic link at the final file path so that a link
// committed into a scanned tree cannot redirect the report onto another file.
func outputBytesToFile(outputDest, defaultName, extension string, bytes []byte) error {
	if outputDest == "" {
		return errors.New("outputDest is an empty string")
	}

	fileName := outputDest
	info, err := os.Stat(outputDest)
	switch {
	case err == nil && info.IsDir():
		if extension != "" {
			extension = "." + extension
		}
		fileName = outputDest + "/" + defaultName + extension
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("failed to create a file: %w", err)
	}

	if linfo, err := os.Lstat(fileName); err == nil && linfo.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("failed to create a file: refusing to write report to symbolic link %s", fileName)
	}

	file, err := os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|openNoFollow, 0o666)
	if err != nil {
		return fmt.Errorf("failed to create a file: %w", err)
	}
	defer file.Close()
	_, err = file.Write(bytes)
	if err != nil {
		return fmt.Errorf("failed to output bytes to a file: %w", err)
	}
	return nil
}
