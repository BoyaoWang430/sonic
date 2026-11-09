//go:build go1.21

package rt

import "unsafe"

// These helpers also have Go 1.20 implementations, which cannot use the new
// unsafe builtins under the module's Go 1.18 language version.
func StringData(s string) *byte    { return unsafe.StringData(s) }
func SliceData[T any](s []T) *T    { return unsafe.SliceData(s) }
func String(p *byte, n int) string { return unsafe.String(p, n) }
