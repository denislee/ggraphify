//go:build !linux || !cgo

package ui

// mallocTrim is a no-op where there is no glibc to trim.
func mallocTrim() bool { return false }
