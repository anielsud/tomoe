//go:build !darwin

package main

// setupLogFile leaves output where it is on Linux, where the app is
// normally started from a terminal or a desktop launcher that keeps it.
func setupLogFile() {}
