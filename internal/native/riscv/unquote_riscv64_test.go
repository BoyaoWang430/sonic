//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

func stringDecodeOracle(t *testing.T, body string) string {
	t.Helper()
	var text string
	if err := json.Unmarshal([]byte(`"`+body+`"`), &text); err != nil {
		t.Fatalf("standard-library oracle rejected %q: %v", body, err)
	}
	return text
}

func checkUnquote(t *testing.T, src, want string, flags uint64, wantPos int, wantErr types.ParsingError) {
	t.Helper()
	for _, alias := range []bool{false, true} {
		storage := bytes.Repeat([]byte{0xa5}, len(src)+32)
		dst := storage[16 : 16+len(src)]
		input := src
		if alias {
			copy(dst, src)
			input = unsafe.String(unsafe.SliceData(dst), len(dst))
		}
		written, pos, err := Unquote(input, dst, flags)
		if err != wantErr || pos != wantPos || written < 0 || written > len(dst) {
			t.Fatalf("Unquote(%q, flags=%d alias=%v)=(%d,%d,%v), want pos=%d err=%v", src, flags, alias, written, pos, err, wantPos, wantErr)
		}
		if wantErr == 0 && string(dst[:written]) != want {
			t.Fatalf("Unquote(%q, flags=%d alias=%v)=%q, want %q", src, flags, alias, dst[:written], want)
		}
		if !bytes.Equal(storage[:16], bytes.Repeat([]byte{0xa5}, 16)) || !bytes.Equal(storage[16+len(src):], bytes.Repeat([]byte{0xa5}, 16)) {
			t.Fatal("Unquote wrote outside dst")
		}
		if !alias && !bytes.Equal(dst[written:], bytes.Repeat([]byte{0xa5}, len(dst)-written)) {
			t.Fatal("Unquote wrote beyond the reported output")
		}
	}
}

func TestUnquoteNativeCases(t *testing.T) {
	t.Logf("toolchain=%s scan width=%d bits", runtime.Version(), stringDecodeVectorBytes()*8)
	for _, tt := range []struct {
		src, want string
		flags     uint64
	}{
		{"", "", 0},
		{"plain中文", "plain中文", 0},
		{"\xff\xc0\x80\x00\n\"", "\xff\xc0\x80\x00\n\"", 0},
		{`hello\b\f\n\r\t\\\"\u2333world`, "hello\b\f\n\r\t\\\"\u2333world", 0},
		{`hello\\b\\f\\n\\r\\t\\\\\\\"\\u2333world`, "hello\b\f\n\r\t\\\"\u2333world", types.F_DOUBLE_UNQUOTE},
		{`hello\ud800world`, "hello\ufffdworld", types.F_UNICODE_REPLACE},
		{`hello\ud800\ud800world`, "hello\ufffd\ufffdworld", types.F_UNICODE_REPLACE},
		{`\ud800\u0041`, "\ufffdA", types.F_UNICODE_REPLACE},
		{`\ud800\ud800\udc00`, "\ufffd\U00010000", types.F_UNICODE_REPLACE},
		{`\udfff\ud800\udc00`, "\ufffd\U00010000", types.F_UNICODE_REPLACE},
		{`\udbff\udfff`, "\U0010ffff", 0},
		{`\/\u0000\u007f\u0080\u07ff\u0800\uffff`, "/\x00\x7f\u0080\u07ff\u0800\uffff", 0},
		{`\\ud800`, "\ufffd", types.F_DOUBLE_UNQUOTE | types.F_UNICODE_REPLACE},
	} {
		checkUnquote(t, tt.src, tt.want, tt.flags, -1, 0)
	}
	for _, tt := range []struct {
		src   string
		flags uint64
		pos   int
		err   types.ParsingError
	}{
		{`asdf\`, 0, 5, types.ERR_EOF},
		{`asdf\gqwer`, 0, 5, types.ERR_INVALID_ESCAPE},
		{`asdf\u1gggqwer`, 0, 7, types.ERR_INVALID_CHAR},
		{`asdf\ud800qwer`, 0, 6, types.ERR_INVALID_UNICODE},
		{`asdf\\ud800qwer`, types.F_DOUBLE_UNQUOTE, 6, types.ERR_INVALID_UNICODE},
		{`asdf\ud800\ud800qwer`, 0, 12, types.ERR_INVALID_UNICODE},
		{`asdf\\ud800\\ud800qwer`, types.F_DOUBLE_UNQUOTE, 14, types.ERR_INVALID_UNICODE},
		{`\u`, 0, 2, types.ERR_EOF},
		{`\u123`, 0, 5, types.ERR_EOF},
		{`\u12x4`, 0, 4, types.ERR_INVALID_CHAR},
		{`\ud800\u12x4`, types.F_UNICODE_REPLACE, 10, types.ERR_INVALID_CHAR},
		{`\'`, 0, 1, types.ERR_INVALID_ESCAPE},
		{`\n`, types.F_DOUBLE_UNQUOTE, 2, types.ERR_EOF},
		{`\\\`, types.F_DOUBLE_UNQUOTE, 3, types.ERR_EOF},
		{`\\\x`, types.F_DOUBLE_UNQUOTE, 3, types.ERR_INVALID_CHAR},
		{`\\ud800`, types.F_DOUBLE_UNQUOTE, 7, types.ERR_EOF},
	} {
		checkUnquote(t, tt.src, "", tt.flags, tt.pos, tt.err)
	}
}

func TestUnquoteFallbackUnicode(t *testing.T) {
	// Reuse encoding/json, the community fallback's source of Unicode
	// behavior. Importing public unquote here would form an import cycle once
	// the candidate routes unquote back to rvv. No new reference algorithm is
	// introduced. This covers every BMP code unit, including all surrogates.
	for value := 0; value <= 0xffff; value++ {
		src := fmt.Sprintf(`\u%04x`, value)
		want := stringDecodeOracle(t, src)
		checkUnquote(t, src, want, types.F_UNICODE_REPLACE, -1, 0)
	}
	rng := rand.New(rand.NewSource(20261023))
	for i := 0; i < 2000; i++ {
		src := fmt.Sprintf(`\u%04x\u%04x\u%04x`, 0xd800+rng.Intn(0x800), rng.Intn(0x10000), rng.Intn(0x10000))
		want := stringDecodeOracle(t, src)
		checkUnquote(t, src, want, types.F_UNICODE_REPLACE, -1, 0)
		double, errJSON := json.Marshal(src)
		if errJSON != nil {
			t.Fatal(errJSON)
		}
		checkUnquote(t, string(double[1:len(double)-1]), want, types.F_DOUBLE_UNQUOTE|types.F_UNICODE_REPLACE, -1, 0)
	}
}

func TestUnquoteVectorBoundariesAndDirectEntry(t *testing.T) {
	vl := stringDecodeVectorBytes()
	for prefix := 0; prefix <= 3*vl+1; prefix++ {
		for _, suffix := range []string{``, `\n`, `\u4e2d`, `\ud800\udc00`, `\\\"end`} {
			src := strings.Repeat("x", prefix) + suffix + strings.Repeat("z", vl+1)
			want := stringDecodeOracle(t, src)
			checkUnquote(t, src, want, types.F_UNICODE_REPLACE, -1, 0)
			for align := 0; align < 16; align++ {
				dst := make([]byte, align+len(src))[align:]
				end, written, _, pos, code := stringDecode(unsafe.StringData(src), len(src), unsafe.SliceData(dst), 2)
				if end != len(src) || code != 0 || pos != -1 || string(dst[:written]) != want {
					t.Fatalf("direct assembly prefix=%d align=%d: end=%d written=%d pos=%d code=%d", prefix, align, end, written, pos, code)
				}
			}
		}
	}
	end, written, escaped, pos, code := stringDecode(nil, 0, nil, 0)
	if end != 0 || written != 0 || escaped != 0 || pos != -1 || code != 0 {
		t.Fatalf("zero-length direct assembly: %d %d %d %d %d", end, written, escaped, pos, code)
	}
}
