//go:build linux

package alg

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"syscall"
	"testing"
	"unsafe"
)

func newAlgGuardPage(t *testing.T) []byte {
	t.Helper()
	page := os.Getpagesize()
	mapping, err := syscall.Mmap(-1, 0, 3*page, syscall.PROT_NONE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Munmap(mapping); err != nil {
			t.Error(err)
		}
	})
	middle := mapping[page : 2*page]
	if err := syscall.Mprotect(middle, syscall.PROT_READ|syscall.PROT_WRITE); err != nil {
		t.Fatal(err)
	}
	return middle
}

func guardedStringAtEdges(srcPage []byte, s string) [2]string {
	n := len(s)
	if n == 0 {
		return [2]string{"", ""}
	}
	copy(srcPage[:n], s)
	sFront := unsafe.String(&srcPage[0], n)
	start := len(srcPage) - n
	copy(srcPage[start:], s)
	sBack := unsafe.String(&srcPage[start], n)
	return [2]string{sFront, sBack}
}

func TestQuoteAndPaddedKeyGuardPages(t *testing.T) {
	srcPage := newAlgGuardPage(t)
	dstPage := newAlgGuardPage(t)

	var paddedShort [32]byte
	shortKey := `"k":`
	copy(paddedShort[:], shortKey)

	var paddedLong [32]byte
	longKey := `,"twenty_char_field":`
	copy(paddedLong[:], longKey)

	alphabet := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789:/._-?"
	makeClean := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[i%len(alphabet)]
		}
		return string(b)
	}

	for n := 0; n <= 96; n++ {
		clean := makeClean(n)
		variants := []string{clean}
		if n > 0 {
			for _, pos := range []int{0, n / 2, n - 1} {
				for _, esc := range []byte{'"', '\\', '\n', 0x1f} {
					buf := []byte(clean)
					buf[pos] = esc
					variants = append(variants, string(buf))
				}
			}
		}

		for _, raw := range variants {
			wantJSON, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, gs := range guardedStringAtEdges(srcPage, raw) {
				if capNeed := 34 + n; capNeed <= len(dstPage) {
					for _, kp := range []struct {
						ptr *byte
						key string
					}{
						{&paddedShort[0], shortKey},
						{&paddedLong[0], longKey},
					} {
						dst := dstPage[len(dstPage)-capNeed : len(dstPage)-capNeed : len(dstPage)]
						got := QuoteWithPaddedKey(dst, kp.ptr, len(kp.key), gs)
						if !bytes.HasPrefix(got, []byte(kp.key)) || !bytes.Equal(got[len(kp.key):], wantJSON) {
							t.Fatalf("QuoteWithPaddedKey(len=%d, key=%q) = %s", n, kp.key, got)
						}
					}
				}
			}
		}
	}

	i64Vals := []int64{
		0, 1, 9, 10, 99, 100, 999, 1000, 9999, 10000, 12345678, 123456789,
		math.MaxInt32, math.MinInt32, math.MaxInt64, math.MinInt64,
		-1, -9, -10, -99, -100, -999, -1000, -9999, -10000, -123456789,
	}
	for _, capNeed := range []int{52, 51, 24} {
		for _, v := range i64Vals {
			dst := dstPage[len(dstPage)-capNeed : len(dstPage)-capNeed : len(dstPage)]
			got := AppendPaddedKeyI64(dst, &paddedLong[0], len(longKey), v)
			want := longKey + strconv.FormatInt(v, 10)
			if string(got) != want {
				t.Fatalf("AppendPaddedKeyI64(cap=%d, v=%d) = %q, want %q", capNeed, v, got, want)
			}
		}
	}
	u64Vals := []uint64{
		0, 1, 9, 10, 99, 100, 999, 1000, 9999, 10000, 12345678, 123456789,
		math.MaxUint32, math.MaxInt64, math.MaxUint64,
	}
	for _, capNeed := range []int{52, 51, 24} {
		for _, u := range u64Vals {
			dst := dstPage[len(dstPage)-capNeed : len(dstPage)-capNeed : len(dstPage)]
			got := AppendPaddedKeyU64(dst, &paddedLong[0], len(longKey), u)
			want := longKey + strconv.FormatUint(u, 10)
			if string(got) != want {
				t.Fatalf("AppendPaddedKeyU64(cap=%d, u=%d) = %q, want %q", capNeed, u, got, want)
			}
		}
	}
}
