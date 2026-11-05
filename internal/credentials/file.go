// Package credentials reads permission-restricted credential files.
package credentials

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ReadRestrictedFile returns at most 64 KiB from a regular owner-readable file.
// The file must have no group or other permission bits.
func ReadRestrictedFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect credential file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0400 == 0 {
		return nil, errors.New("credential file must be regular and readable by its owner only")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open credential file: %w", err)
	}
	// A close failure cannot invalidate bytes from this read-only descriptor.
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect open credential file: %w", err)
	}
	if !os.SameFile(info, opened) || opened.Mode() != info.Mode() {
		return nil, errors.New("credential file changed during open")
	}
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return nil, fmt.Errorf("read credential file: %w", err)
	}
	if len(data) > 65536 {
		return nil, errors.New("credential file exceeds 64 KiB")
	}
	return data, nil
}
