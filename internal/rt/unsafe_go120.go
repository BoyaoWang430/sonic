//go:build go1.20 && !go1.21

package rt

import "unsafe"

// Go 1.20 checks unsafe.String/StringData/SliceData against the module's Go
// language version (1.18), rather than a file's build constraints. Keep the
// equivalent header operations here until per-file language versions apply.
func StringData(s string) *byte { return (*byte)((*GoString)(unsafe.Pointer(&s)).Ptr) }
func SliceData[T any](s []T) *T { return (*T)((*GoSlice)(unsafe.Pointer(&s)).Ptr) }
func String(p *byte, n int) string {
	if n < 0 || (p == nil && n != 0) {
		panic("rt.String: invalid pointer or length")
	}
	return StrFrom(unsafe.Pointer(p), int64(n))
}
