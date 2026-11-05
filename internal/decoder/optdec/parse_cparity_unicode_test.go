//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unsafe"
)

func checkCParityUnicode(t *testing.T, src, want string, consumed, code, gap int, consecutive bool) {
	t.Helper()
	// Both overlapping output arrangements occur when earlier escapes have
	// already shortened the string. The padded suffix permits C-style reads.
	buf := make([]byte, gap+len(src)+64)
	for i := range buf {
		buf[i] = 0xa5
	}
	copy(buf[gap:], src)
	before := append([]byte(nil), buf...)
	base := unsafe.Pointer(&buf[0])
	s, d, gotCode := cParityDecodeUnicode(unsafe.Add(base, gap), base, consecutive)
	if gotCode != code || uintptr(s)-uintptr(base) != uintptr(gap+consumed) || uintptr(d)-uintptr(base) != uintptr(len(want)) {
		t.Fatalf("src=%q gap=%d consecutive=%v: code/cursors=%d/%d/%d want %d/%d/%d", src, gap, consecutive,
			gotCode, uintptr(s)-uintptr(base), uintptr(d)-uintptr(base), code, gap+consumed, len(want))
	}
	copy(before, want)
	if !bytes.Equal(buf, before) {
		t.Fatalf("src=%q gap=%d consecutive=%v: buffer=%q want=%q", src, gap, consecutive, buf, before)
	}
}

func TestCParityUnicodeHex(t *testing.T) {
	check := func(text [4]byte) {
		t.Helper()
		p := unsafe.Pointer(&text[0])
		value, valid := cParityReadHex16(p)
		// The old C SWAR implementation remains used by the other C-parity
		// backends. Preserve its acceptance and value on arbitrary bytes.
		if wantValid := cParityHex16Valid(p); valid != wantValid || valid && value != cParityHex16(p) {
			t.Fatalf("hex %x: got %x/%v, want %x/%v", text, value, valid, cParityHex16(p), wantValid)
		}
	}
	for _, text := range []string{"0000", "9999", "aaaa", "ffff", "AAAA", "FFFF", "09aF", "d800", "dfff"} {
		for position := 0; position < 4; position++ {
			for b := 0; b < 256; b++ {
				var mutated [4]byte
				copy(mutated[:], text)
				mutated[position] = byte(b)
				check(mutated)
			}
		}
	}
	rng := rand.New(rand.NewSource(71553))
	for i := 0; i < 1<<18; i++ {
		v := rng.Uint32()
		check([4]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
	}
}

func TestCParityUnicodeCodeUnits(t *testing.T) {
	// encoding/json supplies an independent UTF-16/UTF-8 reference, including
	// replacement of each isolated surrogate. Check every UTF-16 code unit.
	for value := 0; value <= 0xffff; value++ {
		src := fmt.Sprintf(`\u%04x`, value)
		var want string
		if err := json.Unmarshal([]byte(`"`+src+`"`), &want); err != nil {
			t.Fatal(err)
		}
		checkCParityUnicode(t, src+`"`, want, 6, 0, value%8, true)
	}
}

func TestCParityUnicodeRuns(t *testing.T) {
	for _, tc := range []struct {
		src, want string
		consumed  int
		code      int
	}{
		{`\u0041\u007f\u0080\u07ff\u0800\uffff"`, "A\x7f\u0080\u07ff\u0800\uffff", 36, 0},
		{`\uD83D\uDe00\u4E2d\u6587"`, "😀中文", 24, 0},
		{`\ud800\ud800\udc00"`, "\ufffd\U00010000", 18, 0},
		{`\ud800\u0041\udc00"`, "\ufffdA\ufffd", 18, 0},
		{`\ud800x\u0041"`, "\ufffd", 6, 0},
		{`\u0041\n\u0042"`, "A", 6, 0},
		{`\u00xx"`, "", 0, SONIC_INVALID_ESCAPED_UTF},
		{`\ud800\u00xx"`, "", 6, SONIC_INVALID_ESCAPED_UTF},
		{`\u0041\u00xx"`, "A", 6, SONIC_INVALID_ESCAPED_UTF},
		{`\ud800\u0041\u00xx"`, "\ufffdA", 12, SONIC_INVALID_ESCAPED_UTF},
		{`\ud800\ud800\u00xx"`, "\ufffd", 12, SONIC_INVALID_ESCAPED_UTF},
	} {
		for _, gap := range []int{0, 1, 7, 31} {
			checkCParityUnicode(t, tc.src, tc.want, tc.consumed, tc.code, gap, true)
		}
	}
	// The shared single-operation entry must stop before an independent next
	// escape, while retaining C's consumption of a non-low second surrogate.
	checkCParityUnicode(t, `\u0041\u0042"`, "A", 6, 0, 0, false)
	checkCParityUnicode(t, `\ud800\u0041\u0042"`, "\ufffdA", 12, 0, 3, false)

	rng := rand.New(rand.NewSource(2601103))
	for trial := 0; trial < 512; trial++ {
		var body strings.Builder
		for unit := 0; unit < 64; unit++ {
			value := rng.Intn(65536)
			if unit%3 == 0 {
				value = 0xd800 + rng.Intn(2048)
			}
			fmt.Fprintf(&body, `\u%04X`, value)
		}
		src := body.String()
		var want string
		if err := json.Unmarshal([]byte(`"`+src+`"`), &want); err != nil {
			t.Fatal(err)
		}
		checkCParityUnicode(t, src+`"`, want, len(src), 0, trial%32, true)
	}
}
