//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

// The selected value starts at a known offset, so its oracle does not reuse
// GetByPath's final scanner. Keep the states between calls to cover reuse after
// both successful scans and failures with a nonempty grammar stack.
type pathHandoffStates struct {
	got, want types.StateMachine
}

func (states *pathHandoffStates) check(t *testing.T, src string, start, selected int, path []interface{}) {
	t.Helper()
	for _, strict := range []bool{false, true} {
		gotPos, wantPos := start, selected
		var state *types.StateMachine
		var want int
		if strict {
			state = &states.got
			want = validateOneState(&src, &wantPos, &states.want, 0)
		} else {
			want = SkipOneFast(&src, &wantPos)
		}
		got := GetByPath(&src, &gotPos, &path, state)
		if got != want || gotPos != wantPos {
			t.Fatalf("src=%q start=%d selected=%d path=%#v strict=%v: (%d,%d), want (%d,%d)", src, start, selected, path, strict, got, gotPos, want, wantPos)
		}
		if strict && (states.got.Sp != states.want.Sp || !slices.Equal(states.got.Vt[:states.got.Sp], states.want.Vt[:states.want.Sp])) {
			t.Fatalf("src=%q path=%#v: selected-value grammar stack differs", src, path)
		}
	}
}

func TestRISCVPathSelectedHandoff(t *testing.T) {
	var states pathHandoffStates
	for _, width := range []int{0, 1, 7, 8, 9, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129} {
		key := strings.Repeat("k", width)
		for offset := 0; offset < 8; offset++ {
			for _, selected := range []string{
				`""`, `"a\nb\\c\"d"`, `"\q"`, `"\u12"`,
				`["x",{"k":"a\"b"},-12.345e+67]`, `{"s":"\n"}`,
				`"unterminated`, `"trailing\`, `[1,]`, `{"k":`, "",
			} {
				// A key's closing quote can share a word with a later backslash.
				// Its first event must win, even when cache state survives into
				// the selected short string or container.
				prefix := strings.Repeat("x", offset) + `{"noise":"a\"b","` + key + `":`
				src := prefix + selected
				states.check(t, src, offset, len(prefix), []interface{}{key})
				// Also select through an array, after relaxed malformed values
				// have populated the path cache.
				prefix = strings.Repeat("x", offset) + `{"` + key + `":[nulX,[invalid],`
				src = prefix + selected
				states.check(t, src, offset, len(prefix), []interface{}{key, 2})
			}
		}
	}
}

func TestRISCVPathSelectedHandoffRelaxedAndWhitespace(t *testing.T) {
	var states pathHandoffStates
	for _, noise := range []string{
		`"noise":1e+`, `"noise":nulX`, `"noise":[invalid]`,
		`"x\q":0`, `"\u0078\q":0`, `"noise":"\q"`,
	} {
		for _, gap := range []int{0, 1, 7, 8, 15, 16, 17, 31, 32, 63, 64, 65} {
			white := strings.Repeat(" \t\r\n", (gap+3)/4)[:gap]
			prefix := `{"` + strings.Repeat("p", gap) + `":0,` + noise + `,` + white + `"target"` + white + `:`
			for _, tail := range []string{white + `"a\nb\\c\"d"}`, white, white + `{"x":`, white + "\x00"} {
				src := prefix + tail
				states.check(t, src, 0, len(prefix), []interface{}{"target"})
			}
		}
	}
}

func TestRISCVPathSelectedHandoffDepth(t *testing.T) {
	var states pathHandoffStates
	for _, depth := range []int{255, 256, 257} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			const prefix = `{"noise":"a\"b","target":`
			body := strings.Repeat("[", depth) + `"leaf"` + strings.Repeat("]", depth)
			for _, cut := range []int{len(body), depth + 3, len(body) - 1, len(body)} {
				src := prefix + body[:cut]
				states.check(t, src, 0, len(prefix), []interface{}{"target"})
			}
			// Path descent itself must not consume the selected value's depth
			// budget; only the remaining target is validated at the handoff.
			path := make([]interface{}, depth+1)
			path[0] = "target"
			for i := 1; i < len(path); i++ {
				path[i] = 0
			}
			states.check(t, prefix+body, 0, len(prefix)+depth, path)
		})
	}
}

func TestRISCVPathTailCacheTransitions(t *testing.T) {
	var states pathHandoffStates
	for _, size := range []int{33, 65, 129} {
		for offset := 0; offset < 8; offset++ {
			for _, leading := range []string{"", strings.Repeat(" ", 9)} {
				// Nine spaces exceed the byte probe and create all masks. The long
				// string then replaces their range with an event-only tail, so
				// stale nonspace/number masks cannot be treated as current.
				head := strings.Repeat("x", offset) + leading + `{"p":"` + strings.Repeat("v", size) + `",`
				for _, gap := range []int{0, 1, 7, 8, 63, 64, 65} {
					white := strings.Repeat(" \t\r\n", (gap+3)/4)[:gap]
					for _, number := range []string{"123456789", "-12.345e+67", "1e+"} {
						// The empty key keeps the number close enough to share
						// the previous string's last word. Unselected 1e+ must
						// retain the relaxed numeric-candidate policy.
						prefix := head + white + `"":` + number + white + `,"target":`
						for _, body := range []string{`"a\nb\\c\"d"}`, `{"a":[1,]}`} {
							states.check(t, prefix+body, offset, len(prefix), []interface{}{"target"})
						}
					}
				}
			}
		}
	}
}

func TestRISCVPathTailCacheExactEvents(t *testing.T) {
	var states pathHandoffStates
	for offset := 0; offset < 8; offset++ {
		for size := 32; size <= 40; size++ {
			for _, key := range []struct{ raw, decoded string }{
				{`"#"`, "#"},
				{`"##"`, "##"},
				{`"\\]"`, `\]`},
				{`"\\]]"`, `\]]`},
				{`"\"#"`, `"#`},
			} {
				// A subtract-based byte-match mask can falsely mark '#' after
				// a quote and ']' after a backslash. These positions become
				// visible after the real event was consumed from a cached tail.
				prefix := strings.Repeat("x", offset) + `{"p":"` + strings.Repeat("v", size) + `",` + key.raw + `:`
				for _, body := range []string{`"ok"}`, `"trailing\`, `{"a":[1,]}`, `["\q",1]}`} {
					states.check(t, prefix+body, offset, len(prefix), []interface{}{key.decoded})
				}
			}
		}
	}
}

func TestRISCVPathTailCacheSearchErrors(t *testing.T) {
	var gotState, wantState types.StateMachine
	path := []interface{}{"target"}
	for size := 32; size <= 40; size++ {
		for _, leading := range []string{"", strings.Repeat(" ", 9)} {
			head := leading + `{"p":"` + strings.Repeat("v", size) + `",`
			for _, tail := range []string{
				`"#"#0,"target":1}`, `"\\]" 0,"target":1}`,
				`"\q":0,"target":1}`, `"\u12x4":0,"target":1}`,
				"\x00\"target\":1}", "\v\"target\":1}",
				`"#"`, `"\\]":`,
			} {
				src := head + tail
				for _, strict := range []bool{false, true} {
					var gotMachine, wantMachine *types.StateMachine
					if strict {
						gotMachine, wantMachine = &gotState, &wantState
					}
					gotPos, wantPos := 0, 0
					// These failures occur before the selected value, so the
					// frozen search is independent of the live target scanner.
					want := frozenGetByPath(&src, &wantPos, &path, wantMachine)
					got := GetByPath(&src, &gotPos, &path, gotMachine)
					if want >= 0 || got != want || gotPos != wantPos {
						t.Fatalf("src=%q strict=%v: (%d,%d), want (%d,%d)", src, strict, got, gotPos, want, wantPos)
					}
					if strict && (gotState.Sp != wantState.Sp || !slices.Equal(gotState.Vt[:gotState.Sp], wantState.Vt[:wantState.Sp])) {
						t.Fatalf("src=%q: search failure changed active grammar stack", src)
					}
				}
			}
		}
	}
}

func TestRISCVPathShortKeySemantics(t *testing.T) {
	cases := []struct {
		src, key string
		err      types.ParsingError
	}{
		{`{"":17}`, "", 0},
		{`{"abcdefg":17}`, "abcdefg", 0},
		{`{"abcdefgh":17}`, "abcdefgh", 0},
		{`{"abcdefghi":17}`, "abcdefghi", 0},
		{`{"abcdefgX":0,"abcdefgh":17}`, "abcdefgh", 0},
		{`{"Xbcdefgh":0,"abcdefgh":17}`, "abcdefgh", 0},
		{`{"abcdXfgh":0,"abcdefgh":17}`, "abcdefgh", 0},
		{`{"abcdefgh":0,"abcdefghi":17}`, "abcdefghi", 0},
		{`{"a\"b":17}`, `a"b`, 0},
		{`{"a\\b":17}`, `a\b`, 0},
		{`{"\"":17}`, `"`, 0},
		{`{"\\":17}`, `\`, 0},
		{`{"\u0061bcdefg":17}`, "abcdefg", 0},
		{"{\"a\x00b\":17}", "a\x00b", 0},
		// An earlier raw quote ends the actual key, even if the requested
		// bytes and another quote could otherwise appear to match together.
		{`{"a"b":17}`, `a"b`, types.ERR_INVALID_CHAR},
		{`{"abcde"f":17}`, `abcde"f`, types.ERR_INVALID_CHAR},
		{`{"abcdef"g":17}`, `abcdef"g`, types.ERR_INVALID_CHAR},
		// An early mismatch must not hide a later string event. Each raw
		// spelling has a quote at the predicted eight-byte key boundary.
		{`{"Xbc"efgh":0,"abcdefgh":17}`, "abcdefgh", types.ERR_INVALID_CHAR},
		{`{"Xbcdefg\":0,"abcdefgh":17}`, "abcdefgh", types.ERR_INVALID_CHAR},
		// The full boundary still matters, but escapes after an established
		// mismatch retain their relaxed comparison semantics.
		{`{"Xbc\qfgh":0,"abcdefgh":17}`, "abcdefgh", 0},
		{`{"abcdefgh" 17}`, "abcdefgh", types.ERR_INVALID_CHAR},
		{`{"abcdefgh":}`, "abcdefgh", types.ERR_INVALID_CHAR},
		{`{"abcdefgh`, "abcdefgh", types.ERR_EOF},
		{`{"abcdefghX":0}`, "abcdefgh", types.ERR_NOT_FOUND},
		// Escape errors only matter when comparison reaches the escape.
		{`{"abcdefg\q":0,"abcdefgh":17}`, "abcdefgh", types.ERR_INVALID_ESCAPE},
		{`{"X\q":0,"abcdefgh":17}`, "abcdefgh", 0},
		// A plain mismatch does not hide later quote/escape boundaries.
		{`{"Xbcdef\q":0,"abcdefgh":17}`, "abcdefgh", 0},
		{`{"abcdef\q":0,"abcdefgh":17}`, "abcdefgh", types.ERR_INVALID_ESCAPE},
		{`{"Xbcde"fg":0,"abcdefgh":17}`, "abcdefgh", types.ERR_INVALID_CHAR},
		{`{"abcdefgh\q":0,"abcdefgh":17}`, "abcdefgh", 0},
	}
	var gotState, wantState types.StateMachine
	for _, preceding := range []string{"", `"prior":"` + strings.Repeat("v", 65) + `",`} {
		for _, tc := range cases {
			src := "prefix " + "{" + preceding + tc.src[1:]
			query := []interface{}{tc.key}
			for _, strict := range []bool{false, true} {
				var gotMachine, wantMachine *types.StateMachine
				if strict {
					gotMachine, wantMachine = &gotState, &wantState
				}
				gotPos, wantPos := len("prefix"), len("prefix")
				want := frozenGetByPath(&src, &wantPos, &query, wantMachine)
				got := GetByPath(&src, &gotPos, &query, gotMachine)
				if tc.err != 0 {
					if want != -int(tc.err) {
						t.Fatalf("oracle src=%q key=%q: error=%d, want %d", src, tc.key, want, -int(tc.err))
					}
				} else if want < 0 || src[want:wantPos] != "17" {
					t.Fatalf("oracle src=%q key=%q: (%d,%d), want raw 17", src, tc.key, want, wantPos)
				}
				if got != want || gotPos != wantPos {
					t.Fatalf("src=%q key=%q strict=%v: (%d,%d), want (%d,%d)", src, tc.key, strict, got, gotPos, want, wantPos)
				}
				if strict && (gotState.Sp != wantState.Sp || !slices.Equal(gotState.Vt[:gotState.Sp], wantState.Vt[:wantState.Sp])) {
					t.Fatalf("src=%q key=%q: active grammar stack differs", src, tc.key)
				}
			}
		}
	}
}
