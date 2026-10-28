//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func assertRISCVTokenParity(t *testing.T, src string, start int, flags uint64, gotState, wantState *types.StateMachine) (int, int) {
	t.Helper()
	gotPos, wantPos := start, start
	want := validateOneState(&src, &wantPos, wantState, flags)
	got := ValidateOne(&src, &gotPos, gotState, flags)
	if got != want || gotPos != wantPos || gotState.Sp != wantState.Sp {
		t.Fatalf("start=%d flags=%d src=%q: token=(%d,%d,%d), state=(%d,%d,%d)", start, flags, src, got, gotPos, gotState.Sp, want, wantPos, wantState.Sp)
	}
	if gotState.Sp < 0 || gotState.Sp > len(gotState.Vt) || !slices.Equal(gotState.Vt[:gotState.Sp], wantState.Vt[:wantState.Sp]) {
		t.Fatalf("start=%d flags=%d src=%q: active grammar stack differs", start, flags, src)
	}
	return got, gotPos
}

func TestRISCVTokenTraversalBoundaries(t *testing.T) {
	var gotState, wantState types.StateMachine
	for _, gap := range []int{0, 1, 7, 8, 9, 15, 16, 17, 31, 32, 33, 63, 64, 65} {
		white := strings.Repeat(" \t\r\n", (gap+3)/4)[:gap]
		for _, ending := range []string{`"`, `\"b"`, `\\"`, `\\\"b"`, `\u0041"`, `\u12"`, `\q"`, "\x00\"", "\xff\""} {
			quoted := `"` + strings.Repeat("a", gap) + ending
			for _, body := range []string{
				"{" + white + quoted + white + ":" + white + `0,"tail":"x"}`,
				`{"key":` + white + quoted + white + `,"tail":0}`,
				"[" + white + quoted + white + `,{"tail":0}]`,
			} {
				for _, offset := range []int{0, 1, 7} {
					src := strings.Repeat("x", offset) + white + body + " trailing"
					ret, pos := assertRISCVTokenParity(t, src, offset, 0, &gotState, &wantState)
					if ret != offset+gap || pos != offset+gap+len(body) || gotState.Sp != 0 {
						t.Fatalf("gap=%d ending=%q: got start=%d end=%d sp=%d", gap, ending, ret, pos, gotState.Sp)
					}
				}
			}
		}
	}
}

func TestRISCVTokenTraversalWhitespaceTail(t *testing.T) {
	var gotState, wantState types.StateMachine
	for _, gap := range []int{0, 1, 15, 16, 17, 18, 64} {
		// The colon shortcut consumes one initial space before the 16-byte
		// whitespace probe, shifting that transition by one byte.
		for _, white := range []string{
			strings.Repeat(" ", gap),
			strings.Repeat(" \t\r\n", (gap+3)/4)[:gap],
		} {
			for _, frame := range [][2]string{{"", ""}, {"[", "]"}, {`{"k":`, "}"}} {
				for _, tail := range []string{"", "\x00null" + frame[1], "\vnull" + frame[1]} {
					src := frame[0] + white + tail
					ret, _ := assertRISCVTokenParity(t, src, 0, 0, &gotState, &wantState)
					if ret >= 0 {
						t.Fatalf("accepted whitespace ending in EOF or an invalid control: %q", src)
					}
				}
			}
		}
	}
}

func TestRISCVTokenTraversalTruncationAndMutation(t *testing.T) {
	var gotState, wantState types.StateMachine
	for _, body := range []string{
		`{"abcdefgh":"ab\"cd\\ef\u0041","n":-12.125e+3,"a":[true,false,null,{},[]]}`,
		"[ \t\r\n{\"long key crossing the thirty-two byte boundary\":[0,\"value\"]},false ]",
		`{"escaped\\\"key":[{"nested":"\\\"tail"}],"last":0}`,
	} {
		// Every incomplete prefix must retain the FSM's exact error cursor and
		// active grammar state, including when scanning began after a prefix.
		for cut := 0; cut <= len(body); cut++ {
			src := "prefix\t " + body[:cut]
			assertRISCVTokenParity(t, src, len("prefix"), 0, &gotState, &wantState)
		}
		for pos := range len(body) {
			for _, replacement := range []byte{0, '\v', '?', '"', '\\', ':', ',', ']', '}'} {
				mutated := []byte(body)
				mutated[pos] = replacement
				assertRISCVTokenParity(t, string(mutated), 0, 0, &gotState, &wantState)
			}
		}
	}
}

func TestRISCVTokenTraversalCursorAndStateReuse(t *testing.T) {
	var gotState, wantState types.StateMachine
	gotState.Sp, wantState.Sp = len(gotState.Vt), len(wantState.Vt)
	for i := range gotState.Vt {
		gotState.Vt[i], wantState.Vt[i] = -1, -1
	}
	const first = `{"a":[{},["x",0]],"b":{"c":true}}`
	const second = `[{"escaped":"a\"b"},null]`
	src := "prefix\t" + first + " \r\n" + second + "\ttrue trailing"
	pos := len("prefix")
	for i, body := range []string{first, second, "true"} {
		ret, end := assertRISCVTokenParity(t, src, pos, 0, &gotState, &wantState)
		if ret < pos || end-ret != len(body) || src[ret:end] != body || gotState.Sp != 0 {
			t.Fatalf("value %d: start=%d end=%d sp=%d", i, ret, end, gotState.Sp)
		}
		pos = end
	}
	// Switch between fast success and failures in the original state machine
	// without resetting the caller-owned grammar buffer.
	for _, tc := range []struct {
		src   string
		flags uint64
	}{
		{`{"a":[1,{"b":`, 0},
		{first, 0},
		{`{"k":"\q"}`, types.F_VALIDATE_STRING},
		{second, 0},
		{`{"a":[?]}`, noValidateJSON},
		{first, 0},
	} {
		assertRISCVTokenParity(t, tc.src, 0, tc.flags, &gotState, &wantState)
	}
	for _, start := range []int{-1, len(src), len(src) + 1} {
		assertRISCVTokenParity(t, src, start, 0, &gotState, &wantState)
	}
}

func TestRISCVTokenTraversalDepthFallback(t *testing.T) {
	for _, depth := range []int{maxTokenDepth - 1, maxTokenDepth, maxTokenDepth + 1} {
		for _, kind := range []string{"array", "object", "mixed"} {
			t.Run(fmt.Sprintf("%s/%d", kind, depth), func(t *testing.T) {
				var prefix strings.Builder
				closing := make([]byte, depth)
				for i := 0; i < depth; i++ {
					if kind == "object" || kind == "mixed" && i%2 == 0 {
						prefix.WriteString(`{"k":`)
						closing[depth-1-i] = '}'
					} else {
						prefix.WriteByte('[')
						closing[depth-1-i] = ']'
					}
				}
				src := prefix.String() + `"leaf"` + string(closing)
				var gotState, wantState types.StateMachine
				ret, pos := assertRISCVTokenParity(t, src, 0, 0, &gotState, &wantState)
				if ret != 0 || pos != len(src) || gotState.Sp != 0 {
					t.Fatalf("valid input at depth %d: start=%d end=%d sp=%d", depth, ret, pos, gotState.Sp)
				}
				for _, cut := range []int{prefix.Len(), prefix.Len() + 3, len(src) - 1} {
					assertRISCVTokenParity(t, src[:cut], 0, 0, &gotState, &wantState)
				}
			})
		}
	}
}

func FuzzRISCVTokenTraversal(f *testing.F) {
	for _, src := range []string{
		`{"a":[1,true,"x"]}`,
		`{"long key crossing the thirty-two byte boundary":"long value crossing the thirty-two byte boundary"}`,
		`{"escaped\\\"key":[{"nested":"a\"b"},null]}`,
		`{"k":"\q"}`,
		`{"a":}`,
		`[1,]`,
		"[\x00]",
		" \t",
	} {
		f.Add(src, uint8(0))
	}
	f.Fuzz(func(t *testing.T, src string, cursor uint8) {
		if len(src) > 4096 {
			return
		}
		var gotState, wantState types.StateMachine
		assertRISCVTokenParity(t, src, 0, 0, &gotState, &wantState)
		if start := int(cursor) % (len(src) + 1); start != 0 {
			assertRISCVTokenParity(t, src, start, 0, &gotState, &wantState)
		}
	})
}
