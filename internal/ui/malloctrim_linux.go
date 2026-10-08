//go:build linux && cgo

package ui

/*
#include <malloc.h>
*/
import "C"

// mallocTrim hands glibc's free arena pages back to the kernel. GTK and Pango
// allocate on the C heap, and the main arena they churn through only ever
// grows: freed chunks stay mapped until something asks glibc to trim. It
// reports whether any memory was released.
func mallocTrim() bool { return C.malloc_trim(0) != 0 }
