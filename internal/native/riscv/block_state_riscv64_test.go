//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import (
	"math/rand"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
)

// This byte-at-a-time oracle intentionally includes malformed JSON: relaxed
// containers count only matching brackets and honor quote escaping outside
// strings. Independent source alignment exercises every whole-word boundary.
func TestContainerBlockState(t *testing.T) {
	rng := rand.New(rand.NewSource(6102601))
	alphabet := []byte("[]{}\"\\\\abc012,: \t\x00\xff")
	storage := make([]byte, 16+258)
	for trial := 0; trial < 20000; trial++ {
		align, n := trial%16, rng.Intn(257)
		input := storage[align : align+n+1]
		input[0] = "[{"[trial%2]
		for i := 1; i < len(input); i++ {
			input[i] = alphabet[rng.Intn(len(alphabet))]
		}
		src := unsafe.String(unsafe.SliceData(storage), align+len(input))
		depth, inside, escaped := 1, false, false
		want, wantErr := len(src), types.ERR_EOF
		for i := align + 1; i < len(src); i++ {
			c := src[i]
			if c == '\\' {
				escaped = !escaped
				continue
			}
			if c == '"' && !escaped {
				inside = !inside
			}
			escaped = false
			if inside {
				continue
			}
			if c == input[0] {
				depth++
			} else if c == input[0]+2 {
				depth--
				if depth == 0 {
					want, wantErr = i+1, 0
					break
				}
			}
		}
		if end, err := SkipContainer(src, align); end != want || err != wantErr {
			t.Fatalf("align=%d input=%q: (%d,%v), want (%d,%v)", align, input, end, err, want, wantErr)
		}
	}
}

type stringBlockResult struct {
	end, escape, errorPos int
	err                   types.ParsingError
}

func stringBlockReference(src string, start int, strict bool) stringBlockResult {
	first := -1
	for i := start + 1; i < len(src); {
		c := src[i]
		if c == '"' {
			return stringBlockResult{i + 1, first, -1, 0}
		}
		if c != '\\' {
			if strict && c < 32 {
				return stringBlockResult{i, first, i, types.ERR_INVALID_CHAR}
			}
			i++
			continue
		}
		if first < 0 {
			first = i
		}
		if i+1 == len(src) {
			break
		}
		if !strict {
			i += 2
			continue
		}
		switch src[i+1] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			i += 2
		case 'u':
			if len(src)-i < 6 {
				return stringBlockResult{len(src), first, len(src), types.ERR_EOF}
			}
			for _, h := range []byte(src[i+2 : i+6]) {
				if h-'0' >= 10 && (h|32)-'a' >= 6 {
					return stringBlockResult{i, first, i, types.ERR_INVALID_CHAR}
				}
			}
			i += 6
		default:
			return stringBlockResult{i, first, i, types.ERR_INVALID_CHAR}
		}
	}
	return stringBlockResult{len(src), first, len(src), types.ERR_EOF}
}

func TestStringBlockEvents(t *testing.T) {
	rng := rand.New(rand.NewSource(6102602))
	alphabet := []byte("\"\\\\/bfnrtuabcdefABCDEF0123456789xz \x00\x1f\xff")
	storage := make([]byte, 16+258)
	for trial := 0; trial < 20000; trial++ {
		align, n := trial%16, rng.Intn(257)
		input := storage[align : align+n+1]
		input[0] = '"'
		for i := 1; i < len(input); i++ {
			input[i] = alphabet[rng.Intn(len(alphabet))]
		}
		src := unsafe.String(unsafe.SliceData(storage), align+len(input))
		for _, strict := range []bool{false, true} {
			want := stringBlockReference(src, align, strict)
			end, escape, errorPos, err := ScanString(src, align, strict)
			got := stringBlockResult{end, escape, errorPos, err}
			if got != want {
				t.Fatalf("align=%d strict=%v input=%q: %+v, want %+v", align, strict, input, got, want)
			}
		}
	}
}
