//go:build !windows

package service

import (
	"os"
	"path/filepath"
)

func replaceFileAtomically(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return nil
	}
	defer func() { _ = dir.Close() }()
	_ = dir.Sync()
	return nil
}
