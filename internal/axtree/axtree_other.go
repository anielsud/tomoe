//go:build !darwin

package axtree

import "errors"

// ErrNotTrusted means the app hasn't been granted Accessibility
// permission, so other apps' trees can't be read.
var ErrNotTrusted = errors.New("axtree: Accessibility permission not granted")

// Read is unsupported off macOS.
func Read(pid int) ([]Element, error) { return nil, errors.New("axtree: macOS only") }
