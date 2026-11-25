//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

/*
 * Copyright 2026 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package native

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/jsonscan"
	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
)

func TestRISCVSkipNativeCases(t *testing.T) {
	src := ` {"asdf": [null, true, false, 1, 2.0, -3]}, 1234.5`
	for _, fast := range []bool{false, true} {
		p := 0
		var start int
		if fast {
			start = SkipOneFast(&src, &p)
		} else {
			start = SkipOne(&src, &p, &types.StateMachine{}, 0)
		}
		if start != 1 || p != 42 {
			t.Fatalf("fast=%v: start=%d end=%d", fast, start, p)
		}
	}
	src = `1 2.5 -3 "asdf\nqwer" true false null {} []`
	p := 0
	m := &types.StateMachine{}
	for _, pair := range [][2]int{{0, 1}, {2, 5}, {6, 8}, {9, 21}, {22, 26}, {27, 32}, {33, 37}, {38, 40}, {41, 43}} {
		if start := SkipOne(&src, &p, m, 0); start != pair[0] || p != pair[1] || m.Sp != 0 {
			t.Fatalf("sequential skip: start=%d end=%d sp=%d; want %v", start, p, m.Sp, pair)
		}
	}
}

func TestRISCVStrictSkipSemantics(t *testing.T) {
	for _, src := range []string{
		"", " \t", "-", "+", "0.", "0. ", "+1", "0.0e ", "9e+", "0e-",
		"tru", "fals", "nul", "trux", "fals ", `"asdf`, `"\\\"`,
		`[1,]`, `{"a":}`, `{"a":1,}`, `{"a" 1}`, `[1 2]`, `[{]}`, "1+2", "1.2.3",
	} {
		p := 0
		if ret := SkipOne(&src, &p, &types.StateMachine{}, 0); ret >= 0 || p < 0 || p > len(src) {
			t.Errorf("invalid %q: ret=%d cursor=%d", src, ret, p)
		}
	}
	for _, src := range []string{`"\q"`, "\"a\nb\"", `{"\q":1}`} {
		p := 0
		if ret := SkipOne(&src, &p, &types.StateMachine{}, 0); ret < 0 {
			t.Errorf("default string scan rejected %q: %d", src, ret)
		}
		p = 0
		if ret := SkipOne(&src, &p, &types.StateMachine{}, types.F_VALIDATE_STRING); ret >= 0 {
			t.Errorf("strict string scan accepted %q", src)
		}
	}
	for _, depth := range []int{types.MAX_RECURSE, types.MAX_RECURSE + 1} {
		src := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
		p := 0
		ret := SkipOne(&src, &p, &types.StateMachine{}, 0)
		if depth == types.MAX_RECURSE && (ret != 0 || p != len(src)) || depth > types.MAX_RECURSE && ret != -int(types.ERR_RECURSE_EXCEED_MAX) {
			t.Errorf("depth=%d ret=%d cursor=%d", depth, ret, p)
		}
	}
}

func TestRISCVFastSkipSemantics(t *testing.T) {
	for _, src := range []string{`[}`, `"abc`, `{{`, `[{`, `{{}`, "tru", "fals"} {
		p := 0
		if ret := SkipOneFast(&src, &p); ret >= 0 || p != len(src) {
			t.Errorf("truncated %q: ret=%d cursor=%d", src, ret, p)
		}
	}
	for _, src := range []string{`txxx`, `nxxx`, `fxxxx`, `1.2.3`, `[invalid,]`, `{]}`, `[}]`, `"\q"`} {
		p := 0
		if ret := SkipOneFast(&src, &p); ret != 0 || p != len(src) {
			t.Errorf("relaxed %q: ret=%d cursor=%d", src, ret, p)
		}
		p = 0
		if ret := SkipOne(&src, &p, &types.StateMachine{}, noValidateJSON); ret != 0 || p != len(src) {
			t.Errorf("NoValidateJSON %q: ret=%d cursor=%d", src, ret, p)
		}
	}
	src := "1 2"
	p := 0
	if ret := SkipOneFast(&src, &p); ret != 0 || p != 1 {
		t.Fatalf("whitespace-delimited fast number: ret=%d cursor=%d", ret, p)
	}
	src = "txxx"
	p = 0
	if ret := ValidateOne(&src, &p, &types.StateMachine{}, noValidateJSON); ret != -int(types.ERR_INVALID_CHAR) || p != 1 {
		t.Fatalf("ValidateOne must still check literal spelling: ret=%d cursor=%d", ret, p)
	}
	for _, bad := range []string{`txxx`, `1.2.3`, `[invalid,]`, `{]}`, `[}]`, `"\q"`} {
		p := 0
		if ret := ValidateOne(&bad, &p, &types.StateMachine{}, types.F_VALIDATE_STRING); ret >= 0 {
			t.Fatalf("strict ValidateOne must reject %q: ret=%d cursor=%d", bad, ret, p)
		}
	}
}

func FuzzRISCVValidateOne(f *testing.F) {
	for _, src := range []string{`null`, `{"a":[1,true,"x"]}`, `{"a":}`, `1e+`, `"\q"`, `"\uD800"`, `[1,]`} {
		f.Add(src)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 4096 || strings.IndexByte(src, 0) >= 0 {
			return
		}
		p := 0
		ret := ValidateOne(&src, &p, &types.StateMachine{}, types.F_VALIDATE_STRING)
		valid := ret >= 0 && p <= len(src) && kernels.SkipSpace(src, p) == len(src)
		if valid != json.Valid([]byte(src)) {
			t.Fatalf("ValidateOne(%q): ret=%d cursor=%d valid=%v", src, ret, p, valid)
		}
	})
}

// Short key shortcuts must preserve every byte and strict-validation result
// of the native scanner, including controls and escaped quotes.
func TestRISCVSkipKeyEvents(t *testing.T) {
	for prefix := 0; prefix <= 16; prefix++ {
		for value := 0; value < 256; value++ {
			for _, suffix := range []string{"", `"`, `" ,0]`, `\\"tail"`} {
				src := `"` + strings.Repeat("a", prefix) + string([]byte{byte(value)}) + suffix
				for _, validate := range []bool{false, true} {
					wantEnd, _, _, wantErr := kernels.ScanString(src, 0, validate)
					pos := 1
					var block traversalBlock
					if err := block.skipKey(src, &pos, validate); pos != wantEnd || err != wantErr {
						t.Fatalf("prefix=%d byte=%#x suffix=%q validate=%v: got (%d,%v), want (%d,%v)", prefix, value, suffix, validate, pos, err, wantEnd, wantErr)
					}
				}
			}
		}
	}
}

func TestRISCVValidateOneSpaceCache(t *testing.T) {
	for _, gap := range []int{0, 1, 4, 7, 31, 63, 64, 65, 129} {
		white := strings.Repeat(" \t\r\n", (gap+3)/4)[:gap]
		body := "{" + white + `"a"` + white + ":" + white + "[" +
			white + `"` + strings.Repeat("x", 79) + `",` + white + `true,` +
			white + `{"b":null}` + white + "]" + white + "}"
		if !json.Valid([]byte(body)) {
			t.Fatal("invalid fixture")
		}
		for _, offset := range []int{0, 1, 7, 63} {
			src := strings.Repeat("x", offset) + white + body + " trailing"
			pos := offset
			var machine types.StateMachine
			start := ValidateOne(&src, &pos, &machine, types.F_VALIDATE_STRING)
			if start != offset+gap || pos != offset+gap+len(body) || machine.Sp != 0 {
				t.Fatalf("gap=%d offset=%d: got start=%d end=%d sp=%d", gap, offset, start, pos, machine.Sp)
			}
		}
		for _, bad := range []byte{0, 1, '\v', '\f', 31} {
			src := "[0," + white + string([]byte{bad}) + "1]"
			pos := 0
			want := -int(types.ERR_INVALID_CHAR)
			if bad == 0 {
				want = -int(types.ERR_EOF)
			}
			if got := ValidateOne(&src, &pos, &types.StateMachine{}, 0); got != want || pos != 4+gap {
				t.Fatalf("gap=%d byte=%#x: got (%d,%d), want (%d,%d)", gap, bad, got, pos, want, 4+gap)
			}
		}
	}
}

func TestValidateOneProgressiveBatchBoundaries(t *testing.T) {
	prefix := strings.Repeat(" ", 128)
	suffix := "," + strings.Repeat(`"pad":"0123456789abcdef",`, 128) + `"end":true}`

	// Valid targets spanning across 64B, 192B, 448B, 960B, and 2048B ramp boundaries
	// (including long cross-block strings and nested containers).
	for _, padLen := range []int{10, 48, 56, 64, 180, 192, 430, 448, 940, 960, 2000} {
		target := `{"s":"` + strings.Repeat("a", padLen) + `","n":123,"ok":[true,null]}`
		doc := prefix + target + suffix
		pos := len(prefix)
		jsonscan.ResetDiag()
		start := ValidateOne(&doc, &pos, nil, 0)
		snap := jsonscan.ReadDiag()
		if start != len(prefix) || pos != len(prefix)+len(target) {
			t.Fatalf("padLen=%d: got=(%d,%d) want=(%d,%d)", padLen, start, pos, len(prefix), len(prefix)+len(target))
		}
		if jsonscan.DiagEnabled && padLen == 10 && snap.ValidateOneLexBytes > 64 {
			t.Fatalf("padLen=%d: expected ValidateOneLexBytes <= 64 with progressive ramp, got %d", padLen, snap.ValidateOneLexBytes)
		}
	}

	// Malformed targets near ramp boundaries must report exact error offsets
	// matching strict F_VALIDATE_STRING (stepGeneral) parity.
	for _, padLen := range []int{50, 175, 430, 945} {
		bad := `{"s":"` + strings.Repeat("b", padLen) + `","n":}`
		doc := prefix + bad + suffix
		pos := len(prefix)
		posStrict := len(prefix)
		got := ValidateOne(&doc, &pos, &types.StateMachine{}, 0)
		want := ValidateOne(&doc, &posStrict, &types.StateMachine{}, types.F_VALIDATE_STRING)
		if got != -int(types.ERR_INVALID_CHAR) || got != want || pos != posStrict {
			t.Fatalf("malformed padLen=%d: got=(%d,%d) want=(%d,%d)", padLen, got, pos, want, posStrict)
		}
	}

	// Truncated target at EOF across ramp boundary.
	trunc := prefix + `{"s":"` + strings.Repeat("c", 200)
	pos := len(prefix)
	if ret := ValidateOne(&trunc, &pos, &types.StateMachine{}, 0); ret != -int(types.ERR_EOF) || pos != len(trunc) {
		t.Fatalf("truncated: got=(%d,%d) want=(%d,%d)", ret, pos, -int(types.ERR_EOF), len(trunc))
	}
}
