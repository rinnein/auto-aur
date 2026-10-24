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

// LockFile is the on-disk shape of auto-aur.lock.json.
//
// Pkgver is the only field older versions wrote; Pkgrel and Fingerprint are
// additive so a lock file from an earlier release still loads.
type LockFile struct {
	Pkgver string `json:"pkgver"`
	// Pkgrel is bumped when upstream content changes without a version change.
	Pkgrel int `json:"pkgrel,omitempty"`
	// Fingerprint identifies the upstream state the files were generated from.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// MarshalLock renders a lock record as JSON with a trailing newline.
func MarshalLock(lf LockFile) (string, error) {
	raw, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal lock file: %w", err)
	}
	return string(raw) + "\n", nil
}

// ReadLock returns the recorded lock record, or the zero value when the file is
// absent.
func ReadLock(path string) (LockFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return LockFile{}, nil
		}
		return LockFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	var lf LockFile
	if err := json.Unmarshal(raw, &lf); err != nil {
		return LockFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return lf, nil
}

// WriteLock persists lf, creating parent directories as needed.
func WriteLock(path string, lf LockFile) error {
	return WriteJSON(path, lf)
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
