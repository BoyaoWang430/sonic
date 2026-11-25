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

package optdec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

var errContractCallbackSentinel = errors.New("contract callback sentinel error")

type contractCallbackProbe struct {
	Value   int
	FailOn  int
	Log     *[]string
	Addrs   *[]uintptr
	FailErr error
}

func (c *contractCallbackProbe) UnmarshalJSON(data []byte) error {
	trimmed := string(bytes.TrimSpace(data))
	if c.Log != nil {
		*c.Log = append(*c.Log, trimmed)
	}
	if c.Addrs != nil {
		*c.Addrs = append(*c.Addrs, uintptr(unsafe.Pointer(c)))
	}
	var v int
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	if c.FailErr != nil && v == c.FailOn {
		return fmt.Errorf("callback rejected %d: %w", v, c.FailErr)
	}
	c.Value = v
	return nil
}

func reinitSameParser(p *Parser, data string, opt uint64) {
	p.Json = data
	p.pos = 0
	p.end = len(p.Json)
	p.options = opt
	p.nbuf.init(p.nodes)
}

func verifyNestedSliceDepth(t *testing.T, label string, root any, wantDepth int, wantLeaf float64) {
	t.Helper()
	cur := root
	for d := 0; d < wantDepth; d++ {
		arr, ok := cur.([]any)
		if !ok {
			t.Fatalf("%s: depth %d expected []any, got %T (%#v)", label, d, cur, cur)
		}
		if len(arr) != 1 {
			t.Fatalf("%s: depth %d expected len 1, got %d", label, d, len(arr))
		}
		cur = arr[0]
	}
	leaf, ok := cur.(float64)
	if !ok || leaf != wantLeaf {
		t.Fatalf("%s: leaf expected float64(%v), got %T (%#v)", label, wantLeaf, cur, cur)
	}
}

// TestContractMatrix10Dimensions validates the 10 public semantic contract
// dimensions defined in the public behavioral contract, independent of internal
// fast-path counts.
func TestContractMatrix10Dimensions(t *testing.T) {
	// Dimensions 3, 4 & 6: Syntax errors inside a top-level value never mutate
	// pre-populated targets or aliased storage or invoke UnmarshalJSON callbacks,
	// and report identical SyntaxError.Pos + Message across all Sonic scan paths.
	t.Run("SyntaxPreMutationAndZeroCallbacks", func(t *testing.T) {
		type env struct {
			First contractCallbackProbe `json:"first"`
			ID    int                   `json:"id"`
			Ptr   *int                  `json:"ptr"`
			Nums  []int                 `json:"nums"`
			Skip  string                `json:"skip"`
		}
		badInputs := []string{
			`{"first":10,"id":20,"ptr":30,"nums":[1,2],"broken":`,
			`{"first":10,"skip":[1,2,truX],"id":20}`,
			`{"first":10,"id":1e+}`,
			`{"first":10,"skip":"\uD800","id":20}`,
		}
		for _, opts := range []uint64{diffStdOpts, diffStrictURCOpts} {
			for _, src := range badInputs {
				if src == `{"first":10,"skip":"\uD800","id":20}` && opts != diffStrictURCOpts {
					continue
				}
				var callsRouter, callsRuns, callsScalar, callsSchema, callsGiant, callsStream []string
				pRouter, pRuns, pScalar, pSchema, pGiant, pStream := 99, 99, 99, 99, 99, 99
				bRouter := [3]int{7, 8, 9}
				bRuns := [3]int{7, 8, 9}
				bScalar := [3]int{7, 8, 9}
				bSchema := [3]int{7, 8, 9}
				bGiant := [3]int{7, 8, 9}
				bStream := [3]int{7, 8, 9}

				makeInit := func(logPtr *[]string, extPtr *int, back *[3]int) func(*env) {
					return func(e *env) {
						e.First = contractCallbackProbe{Value: 77, Log: logPtr}
						e.ID = 88
						e.Ptr = extPtr
						e.Nums = back[:2:3]
						e.Skip = "keep"
					}
				}

				var gotRo env
				makeInit(&callsRouter, &pRouter, &bRouter)(&gotRo)
				sRo, endRo, errRo := DecodeOne(src, opts, &gotRo)
				errRo = checkSingleDocEnd(sRo, endRo, errRo)

				gotR, _, errR := decodeViaRunsPosWithInit[env](src, opts, makeInit(&callsRuns, &pRuns, &bRuns))
				gotSc, _, errSc := decodeViaScalarRunsPosWithInit[env](src, opts, makeInit(&callsScalar, &pScalar, &bScalar))
				gotSh, _, _, errSh := decodeViaSchemaOrFullPosWithInit[env](src, opts, makeInit(&callsSchema, &pSchema, &bSchema))
				gotG, _, errG := decodeViaGiantPosWithInit[env](src, opts, makeInit(&callsGiant, &pGiant, &bGiant))
				gotSt, _, errSt := decodeViaChunkedStreamPosWithInit[env](src, opts, 3, makeInit(&callsStream, &pStream, &bStream))

				for name, err := range map[string]error{
					"router": errRo, "runs": errR, "scalar": errSc, "schema": errSh, "giant": errG, "stream": errSt,
				} {
					if classifyContractErr(err) != "syntax" {
						t.Fatalf("%s (opts=0x%x) on %q: expected SyntaxError, got %T (%v)", name, opts, src, err, err)
					}
				}
				assertSonicErrorDetailParity(t, src, opts, errR, map[string]error{
					"router": errRo, "scalar": errSc, "schema": errSh, "giant": errG, "stream": errSt,
				})
				for name, calls := range map[string][]string{
					"router": callsRouter, "runs": callsRuns, "scalar": callsScalar,
					"schema": callsSchema, "giant": callsGiant, "stream": callsStream,
				} {
					if len(calls) != 0 {
						t.Fatalf("%s invoked UnmarshalJSON %d times on syntax error %q: %v", name, len(calls), src, calls)
					}
				}
				for name, g := range map[string]env{
					"router": gotRo, "runs": gotR, "scalar": gotSc, "schema": gotSh, "giant": gotG, "stream": gotSt,
				} {
					if g.First.Value != 77 || g.ID != 88 || g.Skip != "keep" || *g.Ptr != 99 || !reflect.DeepEqual(g.Nums, []int{7, 8}) {
						t.Fatalf("%s mutated destination on syntax error %q: %+v", name, src, g)
					}
				}
				if pRouter != 99 || pRuns != 99 || pScalar != 99 || pSchema != 99 || pGiant != 99 || pStream != 99 ||
					bRouter != [3]int{7, 8, 9} || bRuns != [3]int{7, 8, 9} || bScalar != [3]int{7, 8, 9} ||
					bSchema != [3]int{7, 8, 9} || bGiant != [3]int{7, 8, 9} || bStream != [3]int{7, 8, 9} {
					t.Fatalf("external aliased storage mutated on syntax error %q", src)
				}
			}
		}
	})

	// Dimensions 4 & 5: Numeric range/type errors leave failed slots unmutated
	// while preserving surrounding field updates, slice backing reuse, and map slot replacement.
	t.Run("PostErrorSlotStateSliceBackingAndMapReplacement", func(t *testing.T) {
		type payload struct {
			Before int            `json:"before"`
			BadNum float64        `json:"bad_num"`
			After  int            `json:"after"`
			Slice  []int          `json:"slice"`
			Map    map[string]int `json:"map"`
		}
		src := `{"before":11,"bad_num":1e999,"unused":{"nested":[1,2,3]},"after":22,"slice":[5,6],"map":{"k":99}}`

		bRo := [4]int{100, 200, 300, 400}
		bRu := [4]int{100, 200, 300, 400}
		bSc := [4]int{100, 200, 300, 400}
		bSh := [4]int{100, 200, 300, 400}
		bGi := [4]int{100, 200, 300, 400}
		bSt := [4]int{100, 200, 300, 400}
		makeInit := func(back *[4]int) func(*payload) {
			return func(p *payload) {
				p.Before = 1
				p.BadNum = 42.5
				p.After = 2
				p.Slice = back[:3:4]
				p.Map = map[string]int{"k": 7, "keep": 8}
			}
		}

		var gotRo payload
		makeInit(&bRo)(&gotRo)
		sRo, endRo, errRo := DecodeOne(src, diffStdOpts, &gotRo)
		errRo = checkSingleDocEnd(sRo, endRo, errRo)

		gotR, _, errR := decodeViaRunsPosWithInit[payload](src, diffStdOpts, makeInit(&bRu))
		gotSc, _, errSc := decodeViaScalarRunsPosWithInit[payload](src, diffStdOpts, makeInit(&bSc))
		gotSh, _, _, errSh := decodeViaSchemaOrFullPosWithInit[payload](src, diffStdOpts, makeInit(&bSh))
		gotGi, _, errGi := decodeViaGiantPosWithInit[payload](src, diffStdOpts, makeInit(&bGi))
		gotSt, _, errSt := decodeViaChunkedStreamPosWithInit[payload](src, diffStdOpts, 4, makeInit(&bSt))

		if classifyContractErr(errR) != "mismatch" {
			t.Fatalf("expected MismatchTypeError for 1e999 -> float64, got %T (%v)", errR, errR)
		}
		assertSonicErrorDetailParity(t, src, diffStdOpts, errR, map[string]error{
			"router": errRo, "scalar": errSc, "schema": errSh, "giant": errGi, "stream": errSt,
		})
		wantBack := [4]int{5, 6, 300, 400}
		if bRo != wantBack || bRu != wantBack || bSc != wantBack || bSh != wantBack || bGi != wantBack || bSt != wantBack {
			t.Fatalf("slice backing mismatch: ro=%v ru=%v sc=%v sh=%v gi=%v st=%v", bRo, bRu, bSc, bSh, bGi, bSt)
		}
		for name, g := range map[string]payload{
			"router": gotRo, "runs": gotR, "scalar": gotSc, "schema": gotSh, "giant": gotGi, "stream": gotSt,
		} {
			if g.Before != 11 || g.BadNum != 42.5 || g.After != 22 ||
				!reflect.DeepEqual(g.Slice, []int{5, 6}) || g.Map["k"] != 99 || g.Map["keep"] != 8 {
				t.Fatalf("%s unexpected post-error state: %+v", name, g)
			}
		}
	})

	// Dimensions 6 & 8: Callback order, receiver address identity, sentinel error
	// contract (errors.Is without brittle address-string matching), and stream Buffered/consumed.
	t.Run("CallbackOrderIdentityErrorContractAndStreamBuffered", func(t *testing.T) {
		type doc struct {
			Items []contractCallbackProbe `json:"items"`
			Extra map[string]any          `json:"extra"`
			Tail  string                  `json:"tail"`
		}
		src := `{"extra":{"u":"\u4e2d\u6587","arr":[true,false,null,-0,123.45]},"items":[10, 20, 30],"tail":"line\nbreak"}`
		var wantLog []string
		var wantAddrs []uintptr
		wantDoc, _, err := decodeViaRunsPosWithInit[doc](src, diffStdOpts, func(d *doc) {
			d.Items = []contractCallbackProbe{
				{Log: &wantLog, Addrs: &wantAddrs},
				{Log: &wantLog, Addrs: &wantAddrs},
				{Log: &wantLog, Addrs: &wantAddrs},
			}
		})
		if err != nil {
			t.Fatalf("runs decode failed: %v", err)
		}
		if !reflect.DeepEqual(wantLog, []string{"10", "20", "30"}) || len(wantAddrs) != 3 {
			t.Fatalf("unexpected callback order/count: log=%v addrs=%v", wantLog, wantAddrs)
		}
		for i := 0; i < 3; i++ {
			if wantAddrs[i] != uintptr(unsafe.Pointer(&wantDoc.Items[i])) {
				t.Fatalf("runs callback %d receiver addr mismatch: got=0x%x want=0x%x", i, wantAddrs[i], uintptr(unsafe.Pointer(&wantDoc.Items[i])))
			}
		}

		for _, chunk := range []int{1, 2, 3, 5, 8, 13, 31, 64} {
			var gotLog []string
			var gotAddrs []uintptr
			gotDoc, consumed, err := decodeViaChunkedStreamPosWithInit[doc](src, diffStdOpts, chunk, func(d *doc) {
				d.Items = []contractCallbackProbe{
					{Log: &gotLog, Addrs: &gotAddrs},
					{Log: &gotLog, Addrs: &gotAddrs},
					{Log: &gotLog, Addrs: &gotAddrs},
				}
			})
			if err != nil || consumed != len(src) {
				t.Fatalf("chunk=%d stream decode failed: err=%v consumed=%d/%d", chunk, err, consumed, len(src))
			}
			if !reflect.DeepEqual(gotLog, wantLog) || len(gotAddrs) != 3 ||
				gotDoc.Items[0].Value != 10 || gotDoc.Items[1].Value != 20 || gotDoc.Items[2].Value != 30 ||
				gotDoc.Tail != wantDoc.Tail || !reflect.DeepEqual(gotDoc.Extra, wantDoc.Extra) {
				t.Fatalf("chunk=%d mismatch: log=%v got=%+v want=%+v", chunk, gotLog, gotDoc, wantDoc)
			}
			for i := 0; i < 3; i++ {
				if gotAddrs[i] != uintptr(unsafe.Pointer(&gotDoc.Items[i])) {
					t.Fatalf("chunk=%d callback %d receiver addr mismatch", chunk, i)
				}
			}
		}

		// Callback error contract (UNJ-PARTIAL): match first error via errors.Is
		// (never brittle pointer text), preserve failed slot, and verify identical continuation
		// and receiver address identity across all 6 Sonic paths.
		initFail20 := func(logPtr *[]string, addrsPtr *[]uintptr) func(*doc) {
			return func(d *doc) {
				d.Items = []contractCallbackProbe{
					{Value: -1, FailOn: 20, FailErr: errContractCallbackSentinel, Log: logPtr, Addrs: addrsPtr},
					{Value: -2, FailOn: 20, FailErr: errContractCallbackSentinel, Log: logPtr, Addrs: addrsPtr},
					{Value: -3, FailOn: 20, FailErr: errContractCallbackSentinel, Log: logPtr, Addrs: addrsPtr},
				}
			}
		}
		var lRo, lRu, lSc, lSh, lGi, lSt []string
		var aRo, aRu, aSc, aSh, aGi, aSt []uintptr
		var dRo doc
		initFail20(&lRo, &aRo)(&dRo)
		_, _, errRo := DecodeOne(src, diffStdOpts, &dRo)
		dRu, _, errRu := decodeViaRunsPosWithInit[doc](src, diffStdOpts, initFail20(&lRu, &aRu))
		dSc, _, errSc := decodeViaScalarRunsPosWithInit[doc](src, diffStdOpts, initFail20(&lSc, &aSc))
		dSh, _, _, errSh := decodeViaSchemaOrFullPosWithInit[doc](src, diffStdOpts, initFail20(&lSh, &aSh))
		dGi, _, errGi := decodeViaGiantPosWithInit[doc](src, diffStdOpts, initFail20(&lGi, &aGi))
		dSt, _, errSt := decodeViaChunkedStreamPosWithInit[doc](src, diffStdOpts, 5, initFail20(&lSt, &aSt))

		for name, err := range map[string]error{
			"router": errRo, "runs": errRu, "scalar": errSc, "schema": errSh, "giant": errGi, "stream": errSt,
		} {
			if !errors.Is(err, errContractCallbackSentinel) {
				t.Fatalf("%s callback error lost sentinel errors.Is contract: %T (%v)", name, err, err)
			}
		}
		for name, item := range map[string]struct {
			d doc
			l []string
			a []uintptr
		}{
			"router": {dRo, lRo, aRo},
			"runs":   {dRu, lRu, aRu},
			"scalar": {dSc, lSc, aSc},
			"schema": {dSh, lSh, aSh},
			"giant":  {dGi, lGi, aGi},
			"stream": {dSt, lSt, aSt},
		} {
			if !reflect.DeepEqual(item.l, []string{"10", "20", "30"}) || len(item.a) != 3 ||
				item.d.Items[0].Value != 10 || item.d.Items[1].Value != -2 || item.d.Items[2].Value != 30 {
				t.Fatalf("%s UNJ-PARTIAL callback order/residual mismatch: log=%v items=%+v", name, item.l, item.d.Items)
			}
			for i := range item.a {
				if item.a[i] != uintptr(unsafe.Pointer(&item.d.Items[i])) {
					t.Fatalf("%s callback %d receiver addr identity mismatch", name, i)
				}
			}
		}

		// Stream multi-value prefix / Buffered residual verification across chunk sizes.
		streamTwo := `{"items":[1],"tail":"a"}  {"items":[2],"tail":"b"}`
		for _, chunk := range []int{1, 3, 7, 19, 64} {
			st := chunkedStreamState{src: streamTwo, chunkSize: chunk}
			var sess *StreamSession
			var first, second doc
			if err := DecodeStream(&st.buf, &st.scanp, diffStdOpts, &first, &sess, unsafe.Pointer(&st), chunkedStreamRefill); err != nil {
				FreeStreamSession(sess)
				t.Fatalf("chunk=%d first stream value failed: %v", chunk, err)
			}
			firstConsumed := st.scanned + st.scanp
			bufferedPrefix := string(st.buf[st.scanp:])
			if firstConsumed != len(`{"items":[1],"tail":"a"}`) || !strings.HasPrefix(`  {"items":[2],"tail":"b"}`, bufferedPrefix) {
				FreeStreamSession(sess)
				t.Fatalf("chunk=%d stream Buffered/consumed mismatch: consumed=%d buffered=%q", chunk, firstConsumed, bufferedPrefix)
			}
			if err := DecodeStream(&st.buf, &st.scanp, diffStdOpts, &second, &sess, unsafe.Pointer(&st), chunkedStreamRefill); err != nil {
				FreeStreamSession(sess)
				t.Fatalf("chunk=%d second stream value failed: %v", chunk, err)
			}
			FreeStreamSession(sess)
			if first.Tail != "a" || second.Tail != "b" {
				t.Fatalf("chunk=%d multi-value stream mismatch: first=%+v second=%+v", chunk, first, second)
			}
		}
	})

	// Dimension 2 & 7: Exact -0.0 signbit preservation and float32 midpoint rounding contract.
	t.Run("NegativeZeroAndFloat32ExactRoundingContract", func(t *testing.T) {
		type numStruct struct {
			F64 float64 `json:"f64"`
			F32 float32 `json:"f32"`
			Any any     `json:"any"`
		}
		for _, negZeroTok := range []string{"-0", "-0.0", "-0e10", "-1e-999"} {
			src := fmt.Sprintf(`{"f64":%s,"f32":%s,"any":%s}`, negZeroTok, negZeroTok, negZeroTok)
			var ro numStruct
			_, _, errRo := DecodeOne(src, diffStdOpts, &ro)
			ru, _, errRu := decodeViaRunsPosWithInit[numStruct](src, diffStdOpts, nil)
			sc, _, errSc := decodeViaScalarRunsPosWithInit[numStruct](src, diffStdOpts, nil)
			sh, _, _, errSh := decodeViaSchemaOrFullPosWithInit[numStruct](src, diffStdOpts, nil)
			gi, _, errGi := decodeViaGiantPosWithInit[numStruct](src, diffStdOpts, nil)
			st, _, errSt := decodeViaChunkedStreamPosWithInit[numStruct](src, diffStdOpts, 3, nil)
			for name, item := range map[string]struct {
				v   numStruct
				err error
			}{
				"router": {ro, errRo}, "runs": {ru, errRu}, "scalar": {sc, errSc},
				"schema": {sh, errSh}, "giant": {gi, errGi}, "stream": {st, errSt},
			} {
				if item.err != nil {
					t.Fatalf("%s on %s failed: %v", name, negZeroTok, item.err)
				}
				if item.v.F64 != 0 || !math.Signbit(item.v.F64) {
					t.Fatalf("%s on %s lost float64 -0.0 signbit: bits=0x%x", name, negZeroTok, math.Float64bits(item.v.F64))
				}
				if item.v.F32 != 0 || !math.Signbit(float64(item.v.F32)) {
					t.Fatalf("%s on %s lost float32 -0.0 signbit: bits=0x%x", name, negZeroTok, math.Float32bits(item.v.F32))
				}
				af, ok := item.v.Any.(float64)
				if !ok || af != 0 || !math.Signbit(af) {
					t.Fatalf("%s on %s lost any float64 -0.0 signbit: %T (%v)", name, negZeroTok, item.v.Any, item.v.Any)
				}
			}
		}

		f32Vectors := []string{
			"1.0000000596046448",
			"1.0000000596046447",
			"-1.0000000596046448",
			"1.401298464324817e-45",
			"3.4028234663852886e+38",
			"3.4028235e+38",
		}
		for _, tok := range f32Vectors {
			want64, err := strconv.ParseFloat(tok, 32)
			if err != nil {
				t.Fatalf("strconv.ParseFloat(%q, 32): %v", tok, err)
			}
			wantBits := math.Float32bits(float32(want64))
			src := fmt.Sprintf(`{"f32":%s}`, tok)
			var ro diffFlatStruct
			_, _, errRo := DecodeOne(src, diffStdOpts, &ro)
			ru, _, errRu := decodeViaRunsPosWithInit[diffFlatStruct](src, diffStdOpts, nil)
			sc, _, errSc := decodeViaScalarRunsPosWithInit[diffFlatStruct](src, diffStdOpts, nil)
			sh, _, _, errSh := decodeViaSchemaOrFullPosWithInit[diffFlatStruct](src, diffStdOpts, nil)
			gi, _, errGi := decodeViaGiantPosWithInit[diffFlatStruct](src, diffStdOpts, nil)
			st, _, errSt := decodeViaChunkedStreamPosWithInit[diffFlatStruct](src, diffStdOpts, 4, nil)
			for name, item := range map[string]struct {
				b   uint32
				err error
			}{
				"router": {math.Float32bits(ro.F32), errRo},
				"runs":   {math.Float32bits(ru.F32), errRu},
				"scalar": {math.Float32bits(sc.F32), errSc},
				"schema": {math.Float32bits(sh.F32), errSh},
				"giant":  {math.Float32bits(gi.F32), errGi},
				"stream": {math.Float32bits(st.F32), errSt},
			} {
				if item.err != nil || item.b != wantBits {
					t.Fatalf("%s float32 bits mismatch on %s: err=%v got=0x%08x want=0x%08x", name, tok, item.err, item.b, wantBits)
				}
			}
		}
	})

	// Explicit separation between RelaxedSkip (OptionNoValidateJSON) and strict validation.
	t.Run("RelaxedSkipSeparatedFromStrictValidation", func(t *testing.T) {
		type sparseTarget struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		malformedUnknown := `{"id":7,"skip_arr":[1,truX,3],"name":"after"}`

		// 1. Exercise relaxed skip first to ensure plan caches are never poisoned.
		var relaxedOut sparseTarget
		_, _, _ = DecodeOne(malformedUnknown, diffNoValValidOpts, &relaxedOut)

		// 2. All strict paths MUST reject malformedUnknown with identical SyntaxError detail and zero mutation.
		initKeep := func(s *sparseTarget) {
			s.ID = 55
			s.Name = "keep"
		}
		var ro sparseTarget
		initKeep(&ro)
		sRo, endRo, errRo := DecodeOne(malformedUnknown, diffStdOpts, &ro)
		errRo = checkSingleDocEnd(sRo, endRo, errRo)
		ru, _, errRu := decodeViaRunsPosWithInit[sparseTarget](malformedUnknown, diffStdOpts, initKeep)
		sc, _, errSc := decodeViaScalarRunsPosWithInit[sparseTarget](malformedUnknown, diffStdOpts, initKeep)
		sh, _, _, errSh := decodeViaSchemaOrFullPosWithInit[sparseTarget](malformedUnknown, diffStdOpts, initKeep)
		gi, _, errGi := decodeViaGiantPosWithInit[sparseTarget](malformedUnknown, diffStdOpts, initKeep)
		st, _, errSt := decodeViaChunkedStreamPosWithInit[sparseTarget](malformedUnknown, diffStdOpts, 5, initKeep)

		if classifyContractErr(errRu) != "syntax" {
			t.Fatalf("strict runs failed to reject malformed unknown subtree: %v", errRu)
		}
		assertSonicErrorDetailParity(t, malformedUnknown, diffStdOpts, errRu, map[string]error{
			"router": errRo, "scalar": errSc, "schema": errSh, "giant": errGi, "stream": errSt,
		})
		for name, g := range map[string]sparseTarget{
			"router": ro, "runs": ru, "scalar": sc, "schema": sh, "giant": gi, "stream": st,
		} {
			if g.ID != 55 || g.Name != "keep" {
				t.Fatalf("strict %s mutated target on malformed unknown subtree: %+v", name, g)
			}
		}
	})

	// Dimensions 9 & 10: Real post-decode verification at depth 4096 vs 4097,
	// plus explicit reuse of the exact same *Parser and *StreamSession instances.
	t.Run("ScalarVsNativeDepthAndExplicitSameInstancePoolReuse", func(t *testing.T) {
		ok4096 := strings.Repeat("[", 4096) + "42.5" + strings.Repeat("]", 4096)
		var vRouter any
		if sRo, endRo, err := DecodeOne(ok4096, diffStdOpts, &vRouter); checkSingleDocEnd(sRo, endRo, err) != nil {
			t.Fatalf("depth 4096 router failed: %v", err)
		}
		vRuns, _, err := decodeViaRunsPosWithInit[any](ok4096, diffStdOpts, nil)
		if err != nil {
			t.Fatalf("depth 4096 runs failed: %v", err)
		}
		vScalar, _, err := decodeViaScalarRunsPosWithInit[any](ok4096, diffStdOpts, nil)
		if err != nil {
			t.Fatalf("depth 4096 scalar failed: %v", err)
		}
		vGiant, _, err := decodeViaGiantPosWithInit[any](ok4096, diffStdOpts, nil)
		if err != nil {
			t.Fatalf("depth 4096 giant failed: %v", err)
		}
		vStream, _, err := decodeViaChunkedStreamPosWithInit[any](ok4096, diffStdOpts, 257, nil)
		if err != nil {
			t.Fatalf("depth 4096 stream failed: %v", err)
		}
		verifyNestedSliceDepth(t, "router", vRouter, 4096, 42.5)
		verifyNestedSliceDepth(t, "runs", vRuns, 4096, 42.5)
		verifyNestedSliceDepth(t, "scalar", vScalar, 4096, 42.5)
		verifyNestedSliceDepth(t, "giant", vGiant, 4096, 42.5)
		verifyNestedSliceDepth(t, "stream", vStream, 4096, 42.5)

		over4097 := strings.Repeat("[", 4097) + "1" + strings.Repeat("]", 4097)
		var badRo any
		sRo, endRo, errRo := DecodeOne(over4097, diffStdOpts, &badRo)
		errRo = checkSingleDocEnd(sRo, endRo, errRo)
		_, _, errR := decodeViaRunsPosWithInit[any](over4097, diffStdOpts, nil)
		_, _, errS := decodeViaScalarRunsPosWithInit[any](over4097, diffStdOpts, nil)
		_, _, errGi := decodeViaGiantPosWithInit[any](over4097, diffStdOpts, nil)
		_, _, errSt := decodeViaChunkedStreamPosWithInit[any](over4097, diffStdOpts, 64, nil)
		if pos, _, ok := contractSyntaxDetail(errR); !ok || pos != 4096 {
			t.Fatalf("depth 4097 expected SyntaxError at Pos=4096, got pos=%d err=%v", pos, errR)
		}
		assertSonicErrorDetailParity(t, over4097, diffStdOpts, errR, map[string]error{
			"router": errRo, "scalar": errS, "giant": errGi, "stream": errSt,
		})

		// Explicit reuse of the EXACT SAME *Parser instance across:
		// (1) depth 4097 overflow, (2) escaped string + late syntax error,
		// (3) >1.2 MiB oversized tape growth (backup swap), and (4) clean small decode
		// with caller buffer overwritten afterward.
		sameP := newParserImpl(over4097, 0, diffStdOpts)
		origParserAddr := uintptr(unsafe.Pointer(sameP))
		if _, code := sameP.scanDocument(diffStdOpts, anyType, nil); code != SONIC_STACK_OVERFLOW {
			sameP.free()
			t.Fatalf("sameP step 1 expected SONIC_STACK_OVERFLOW, got %v", code)
		}
		sameP.reset()

		badEscLate := `{"name":"hello\nworld","nums":[1,2,`
		reinitSameParser(sameP, badEscLate, diffStdOpts)
		if uintptr(unsafe.Pointer(sameP)) != origParserAddr {
			t.Fatal("sameP address unexpectedly changed")
		}
		if _, code := sameP.scanDocument(diffStdOpts, anyType, nil); code == SONIC_OK {
			sameP.free()
			t.Fatalf("sameP step 2 expected syntax failure on truncated input")
		} else if fixErr := fix_error(badEscLate, 0, sameP.fixError(code)); classifyContractErr(fixErr) != "syntax" {
			sameP.free()
			t.Fatalf("sameP step 2 expected SyntaxError, got %v", fixErr)
		}
		sameP.reset()

		// Oversized >1.2 MiB payload (> maxPooledNodesCount) triggers p.backup swap.
		bigCount := maxPooledNodesCount + 4096
		var bigSB strings.Builder
		bigSB.Grow(bigCount * 2)
		bigSB.WriteByte('[')
		for i := 0; i < bigCount; i++ {
			if i > 0 {
				bigSB.WriteByte(',')
			}
			bigSB.WriteByte('1')
		}
		bigSB.WriteByte(']')
		bigJSON := bigSB.String()
		reinitSameParser(sameP, bigJSON, diffStdOpts)
		sameP.presizeNodesOne()
		if _, code := sameP.scanDocument(diffStdOpts, sliceEfaceType, nil); code != SONIC_OK {
			sameP.free()
			t.Fatalf("sameP step 3 oversized scan failed: %v", code)
		}
		sameP.reset()
		if cap(sameP.nodes) > maxPooledNodesCount || cap(sameP.unescBuf) > maxPooledUnescCap || len(sameP.unescBuf) != 0 {
			sameP.free()
			t.Fatalf("sameP reset leaked oversized buffers: nodesCap=%d unescCap=%d unescLen=%d",
				cap(sameP.nodes), cap(sameP.unescBuf), len(sameP.unescBuf))
		}

		// Step 4: Clean decode on the exact same *Parser instance, then wipe caller buffer.
		cleanBuf := []byte(`{"id":314,"name":"reused\tparser","tags":["alpha","beta"]}`)
		cleanStr := unsafe.String(unsafe.SliceData(cleanBuf), len(cleanBuf))
		var reusedOut diffFlatStruct
		etp, vp, dec, _, err := unpackAndCompileRoot(&reusedOut)
		if err != nil {
			sameP.free()
			t.Fatal(err)
		}
		reinitSameParser(sameP, cleanStr, diffStdOpts)
		docSnap, code := sameP.scanDocument(diffStdOpts, etp, nil)
		if code != SONIC_OK {
			sameP.free()
			t.Fatalf("sameP step 4 clean scan failed: %v", code)
		}
		ctx := sameP.initDecodeContext(docSnap, diffStdOpts, etp)
		if err := dec.FromDom(vp, ctx.Root(), ctx); err != nil {
			sameP.free()
			t.Fatalf("sameP step 4 FromDom failed: %v", err)
		}
		sameP.free()
		for i := range cleanBuf {
			cleanBuf[i] = 'X'
		}
		if reusedOut.ID != 314 || reusedOut.Name != "reused\tparser" || !reflect.DeepEqual(reusedOut.Tags, []string{"alpha", "beta"}) {
			t.Fatalf("sameP step 4 result corrupted after pool return + input wipe: %+v", reusedOut)
		}

		// Explicit reuse of the EXACT SAME *StreamSession instance across error -> valid decode.
		sameSess := streamSessionPool.Get().(*StreamSession)
		origSessAddr := uintptr(unsafe.Pointer(sameSess))
		stErr := chunkedStreamState{src: over4097, chunkSize: 128}
		var dummyAny any
		sessArg := sameSess
		if err := DecodeStream(&stErr.buf, &stErr.scanp, diffStdOpts, &dummyAny, &sessArg, unsafe.Pointer(&stErr), chunkedStreamRefill); classifyContractErr(err) != "syntax" {
			FreeStreamSession(sameSess)
			t.Fatalf("sameSess step 1 expected syntax error, got %v", err)
		}
		sameSess.Reset()
		if uintptr(unsafe.Pointer(sameSess)) != origSessAddr {
			t.Fatal("sameSess address changed")
		}
		cleanStreamBuf := []byte(`{"id":2718,"name":"stream\nreuse","nums":[9,8,7]}`)
		stOK := chunkedStreamState{
			src:       unsafe.String(unsafe.SliceData(cleanStreamBuf), len(cleanStreamBuf)),
			chunkSize: 5,
		}
		var streamOut diffFlatStruct
		sessArg = sameSess
		if err := DecodeStream(&stOK.buf, &stOK.scanp, diffStdOpts, &streamOut, &sessArg, unsafe.Pointer(&stOK), chunkedStreamRefill); err != nil {
			FreeStreamSession(sameSess)
			t.Fatalf("sameSess step 2 valid decode failed: %v", err)
		}
		FreeStreamSession(sameSess)
		for i := range cleanStreamBuf {
			cleanStreamBuf[i] = 'Z'
		}
		if streamOut.ID != 2718 || streamOut.Name != "stream\nreuse" || !reflect.DeepEqual(streamOut.Nums, []int{9, 8, 7}) {
			t.Fatalf("sameSess step 2 result corrupted after FreeStreamSession + input wipe: %+v", streamOut)
		}
	})
}

type largeThresholdEnvelope struct {
	Head int    `json:"head"`
	Blob string `json:"blob"`
	Tail int    `json:"tail"`
}

type largeOrderItem struct {
	ID    int64  `json:"id"`
	Buyer string `json:"buyer"`
	Total int64  `json:"total"`
}

func makeExactLengthValidEnvelope(totalLen int) string {
	const prefix = `{"head":101,"blob":"`
	const suffix = `","tail":202}`
	fillLen := totalLen - len(prefix) - len(suffix)
	if fillLen < 0 {
		panic("totalLen too small")
	}
	var sb strings.Builder
	sb.Grow(totalLen)
	sb.WriteString(prefix)
	sb.WriteString(strings.Repeat("a", fillLen))
	sb.WriteString(suffix)
	return sb.String()
}

func makeExactLengthLateSyntaxErrEnvelope(totalLen int) (string, int) {
	const prefix = `{"head":101,"blob":"`
	const badSuffix = `","tail":truX}`
	fillLen := totalLen - len(prefix) - len(badSuffix)
	if fillLen < 0 {
		panic("totalLen too small")
	}
	var sb strings.Builder
	sb.Grow(totalLen)
	sb.WriteString(prefix)
	sb.WriteString(strings.Repeat("b", fillLen))
	wantErrPos := sb.Len() + len(`","tail":tru`)
	sb.WriteString(badSuffix)
	return sb.String(), wantErrPos
}

// TestLargeThresholdRouting4MiBAnd2p8MB deterministically covers the exact
// 4 MiB - len(padding) routing boundary ((4<<20)-65, (4<<20)-64, (4<<20)-63,
// (4<<20)-1, 4<<20, (4<<20)+1) as well as ~2.78 MB dense and sparse top-level
// slice payloads.
func TestLargeThresholdRouting4MiBAnd2p8MB(t *testing.T) {
	padBoundary := largeReadOnlyScanBytes - 64
	sizes := []int{
		padBoundary - 1, // (4<<20) - 65
		padBoundary,     // (4<<20) - 64
		padBoundary + 1, // (4<<20) - 63 (crosses len(s)+len(padding) > largeReadOnlyScanBytes)
		largeReadOnlyScanBytes - 1,
		largeReadOnlyScanBytes,
		largeReadOnlyScanBytes + 1,
	}

	for _, sz := range sizes {
		validSrc := makeExactLengthValidEnvelope(sz)
		if len(validSrc) != sz {
			t.Fatalf("expected len %d, got %d", sz, len(validSrc))
		}
		var ro largeThresholdEnvelope
		sRo, endRo, errRo := DecodeOne(validSrc, diffStdOpts, &ro)
		if err := checkSingleDocEnd(sRo, endRo, errRo); err != nil {
			t.Fatalf("size=%d valid router failed: %v", sz, err)
		}
		ru, endRu, errRu := decodeViaRunsPosWithInit[largeThresholdEnvelope](validSrc, diffStdOpts, nil)
		sh, endSh, _, errSh := decodeViaSchemaOrFullPosWithInit[largeThresholdEnvelope](validSrc, diffStdOpts, nil)
		gi, endGi, errGi := decodeViaGiantPosWithInit[largeThresholdEnvelope](validSrc, diffStdOpts, nil)
		if errRu != nil || errSh != nil || errGi != nil {
			t.Fatalf("size=%d valid peer failed: runs=%v schema=%v giant=%v", sz, errRu, errSh, errGi)
		}
		if endRo != sz || endRu != sz || endSh != sz || endGi != sz {
			t.Fatalf("size=%d consumed mismatch: ro=%d ru=%d sh=%d gi=%d", sz, endRo, endRu, endSh, endGi)
		}
		if ro.Head != 101 || ro.Tail != 202 || len(ro.Blob) != len(ru.Blob) || ro != ru || ro != sh || ro != gi {
			t.Fatalf("size=%d value mismatch across paths", sz)
		}

		badSrc, wantPos := makeExactLengthLateSyntaxErrEnvelope(sz)
		initEnv := func(e *largeThresholdEnvelope) {
			e.Head = -7
			e.Blob = "untouched"
			e.Tail = -9
		}
		var badRo largeThresholdEnvelope
		initEnv(&badRo)
		sBadRo, endBadRo, errBadRo := DecodeOne(badSrc, diffStdOpts, &badRo)
		errBadRo = checkSingleDocEnd(sBadRo, endBadRo, errBadRo)
		badRu, _, errBadRu := decodeViaRunsPosWithInit[largeThresholdEnvelope](badSrc, diffStdOpts, initEnv)
		badSh, _, _, errBadSh := decodeViaSchemaOrFullPosWithInit[largeThresholdEnvelope](badSrc, diffStdOpts, initEnv)
		badGi, _, errBadGi := decodeViaGiantPosWithInit[largeThresholdEnvelope](badSrc, diffStdOpts, initEnv)

		if gotPos, _, ok := contractSyntaxDetail(errBadRu); !ok || gotPos != wantPos {
			t.Fatalf("size=%d expected SyntaxError at Pos=%d, got pos=%d err=%v", sz, wantPos, gotPos, errBadRu)
		}
		assertSonicErrorDetailParity(t, "large-4MiB-boundary", diffStdOpts, errBadRu, map[string]error{
			"router": errBadRo, "schema": errBadSh, "giant": errBadGi,
		})
		for name, g := range map[string]largeThresholdEnvelope{
			"router": badRo, "runs": badRu, "schema": badSh, "giant": badGi,
		} {
			if g.Head != -7 || g.Blob != "untouched" || g.Tail != -9 {
				t.Fatalf("size=%d %s mutated pre-populated target on late syntax error: %+v", sz, name, g)
			}
		}
	}

	// ~2.78 MB top-level struct slice (both dense and sparse with unknown subtrees).
	for _, sparse := range []bool{false, true} {
		var sb strings.Builder
		sb.Grow(2_800_000)
		sb.WriteByte('[')
		count := 0
		for sb.Len() < 2_784_000 {
			if count > 0 {
				sb.WriteByte(',')
			}
			if sparse {
				fmt.Fprintf(&sb, `{"id":%d,"buyer":"buyer-%d","extra":{"skip":[1,2,"`+strings.Repeat("x", 180)+`"]},"total":%d}`, count+1, count, (count+1)*10)
			} else {
				fmt.Fprintf(&sb, `{"id":%d,"buyer":"buyer-%d","total":%d}`, count+1, count, (count+1)*10)
			}
			count++
		}
		validSliceJSON := sb.String() + "]"

		var ro []largeOrderItem
		sRo, endRo, errRo := DecodeOne(validSliceJSON, diffStdOpts, &ro)
		if err := checkSingleDocEnd(sRo, endRo, errRo); err != nil {
			t.Fatalf("sparse=%v ~2.78MB valid router failed: %v", sparse, err)
		}
		ru, _, errRu := decodeViaRunsPosWithInit[[]largeOrderItem](validSliceJSON, diffStdOpts, nil)
		sc, _, errSc := decodeViaScalarRunsPosWithInit[[]largeOrderItem](validSliceJSON, diffStdOpts, nil)
		sh, _, _, errSh := decodeViaSchemaOrFullPosWithInit[[]largeOrderItem](validSliceJSON, diffStdOpts, nil)
		gi, _, errGi := decodeViaGiantPosWithInit[[]largeOrderItem](validSliceJSON, diffStdOpts, nil)
		st, _, errSt := decodeViaChunkedStreamPosWithInit[[]largeOrderItem](validSliceJSON, diffStdOpts, 4093, nil)
		if errRu != nil || errSc != nil || errSh != nil || errGi != nil || errSt != nil {
			t.Fatalf("sparse=%v ~2.78MB peer failed: ru=%v sc=%v sh=%v gi=%v st=%v", sparse, errRu, errSc, errSh, errGi, errSt)
		}
		if len(ro) != count || !reflect.DeepEqual(ro, ru) || !reflect.DeepEqual(ro, sc) ||
			!reflect.DeepEqual(ro, sh) || !reflect.DeepEqual(ro, gi) || !reflect.DeepEqual(ro, st) {
			t.Fatalf("sparse=%v ~2.78MB slice value mismatch (len=%d/%d)", sparse, len(ro), count)
		}

		// Late syntax error at the tail of ~2.78 MB slice: zero pre-error mutation + exact Pos parity.
		badSliceJSON := sb.String() + `,{"id":999999,"buyer":truX}]`
		initSlice := func(dst *[]largeOrderItem) {
			*dst = []largeOrderItem{{ID: -42, Buyer: "preserve", Total: -100}}
		}
		var bRo []largeOrderItem
		initSlice(&bRo)
		sBRo, endBRo, errBRo := DecodeOne(badSliceJSON, diffStdOpts, &bRo)
		errBRo = checkSingleDocEnd(sBRo, endBRo, errBRo)
		bRu, _, errBRu := decodeViaRunsPosWithInit[[]largeOrderItem](badSliceJSON, diffStdOpts, initSlice)
		bSc, _, errBSc := decodeViaScalarRunsPosWithInit[[]largeOrderItem](badSliceJSON, diffStdOpts, initSlice)
		bSh, _, _, errBSh := decodeViaSchemaOrFullPosWithInit[[]largeOrderItem](badSliceJSON, diffStdOpts, initSlice)
		bGi, _, errBGi := decodeViaGiantPosWithInit[[]largeOrderItem](badSliceJSON, diffStdOpts, initSlice)
		bSt, _, errBSt := decodeViaChunkedStreamPosWithInit[[]largeOrderItem](badSliceJSON, diffStdOpts, 4093, initSlice)

		assertSonicErrorDetailParity(t, "large-2p8MB-slice", diffStdOpts, errBRu, map[string]error{
			"router": errBRo, "scalar": errBSc, "schema": errBSh, "giant": errBGi, "stream": errBSt,
		})
		wantInit := []largeOrderItem{{ID: -42, Buyer: "preserve", Total: -100}}
		for name, g := range map[string][]largeOrderItem{
			"router": bRo, "runs": bRu, "scalar": bSc, "schema": bSh, "giant": bGi, "stream": bSt,
		} {
			if !reflect.DeepEqual(g, wantInit) {
				t.Fatalf("sparse=%v %s mutated pre-populated slice on ~2.78MB late syntax error: %+v", sparse, name, g)
			}
		}
	}

	// > 4.5 MiB sparse narrow top-level slice ([]sparseOrderID): must STILL be admitted
	// to parseSchemaRoot (no >4MiB full-emit hijack and no blanket slice rejection) and
	// save >90% of tape nodes via ModeValidateOnly while preserving exact syntax error Pos.
	type sparseOrderID struct {
		ID int64 `json:"id"`
	}
	var probeSparseSlice []sparseOrderID
	etpSp, _, _, projSp, errSp := unpackAndCompileRoot(&probeSparseSlice)
	if errSp != nil || projSp == nil {
		t.Fatalf("expected non-nil projPlan for []sparseOrderID: err=%v", errSp)
	}
	var bigSparseSB strings.Builder
	bigSparseSB.Grow(4_800_000)
	bigSparseSB.WriteByte('[')
	elemTemplate := `,"a1":"skip-val-1","a2":[1,2,3,4,5,6,7,8],"a3":{"k1":"v1","k2":"v2","k3":"` + strings.Repeat("w", 160) + `"},"a4":true,"a5":99.5}`
	spCount := 0
	for bigSparseSB.Len() < 4_600_000 {
		if spCount > 0 {
			bigSparseSB.WriteByte(',')
		}
		fmt.Fprintf(&bigSparseSB, `{"id":%d%s`, spCount+1, elemTemplate)
		spCount++
	}
	bigSparseJSON := bigSparseSB.String() + "]"
	if len(bigSparseJSON) <= largeReadOnlyScanBytes {
		t.Fatalf("expected >4MiB sparse slice payload, got %d", len(bigSparseJSON))
	}
	if !projSp.shouldUseOpts(len(bigSparseJSON), diffStdOpts) {
		t.Fatalf("expected >4MiB sparse []sparseOrderID (%dB) to use schema projection", len(bigSparseJSON))
	}

	pSchema := newParserLazy(bigSparseJSON, 0, diffStdOpts)
	pSchema.singleDoc = true
	docSchema, codeSchema := pSchema.scanDocument(diffStdOpts, etpSp, projSp)
	schemaNodes := len(docSchema.nodes)
	pSchema.free()
	if codeSchema != SONIC_OK {
		t.Fatalf(">4MiB sparse slice schema scan failed: %v", codeSchema)
	}

	pFull := newParserImpl(bigSparseJSON, 0, diffStdOpts)
	pFull.singleDoc = true
	pFull.presizeNodesOne()
	docFull, codeFull := pFull.scanDocument(diffStdOpts, etpSp, nil)
	fullNodes := len(docFull.nodes)
	pFull.free()
	if codeFull != SONIC_OK {
		t.Fatalf(">4MiB sparse slice full Runs scan failed: %v", codeFull)
	}
	if schemaNodes*8 > fullNodes {
		t.Fatalf("expected ModeValidateOnly sparse >4MiB slice to save >87.5%% of tape nodes, got schema=%d vs full=%d", schemaNodes, fullNodes)
	}

	var gotSp []sparseOrderID
	sSpOut, endSp, errDecSp := DecodeOne(bigSparseJSON, diffStdOpts, &gotSp)
	if err := checkSingleDocEnd(sSpOut, endSp, errDecSp); err != nil {
		t.Fatalf(">4MiB sparse slice DecodeOne failed: %v", err)
	}
	if len(gotSp) != spCount || gotSp[0].ID != 1 || gotSp[spCount-1].ID != int64(spCount) {
		t.Fatalf(">4MiB sparse slice decoded unexpected elements: len=%d/%d", len(gotSp), spCount)
	}
}
