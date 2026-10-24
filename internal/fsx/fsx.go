// Package fsx holds the small file helpers that were previously duplicated in
// every TypeScript task.
package fsx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// LockFile is the on-disk shape of auto-aur.lock.json. It stays a single-key
// object for backward compatibility with lock files committed by the previous
// TypeScript implementation.
type LockFile struct {
	Pkgver string `json:"pkgver"`
}

// ReadLock returns the recorded pkgver, or "" when the file is absent.
func ReadLock(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var lf LockFile
	if err := json.Unmarshal(raw, &lf); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	return lf.Pkgver, nil
}

// WriteLock persists pkgver, creating parent directories as needed.
func WriteLock(path, pkgver string) error {
	return WriteJSON(path, LockFile{Pkgver: pkgver})
}

// WriteJSON writes v as indented JSON, creating parent directories.
func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// WriteFile writes content with the given mode, creating parent directories.
func WriteFile(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Exists reports whether path exists at all.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// IsDir reports whether path is an existing directory.
func IsDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
