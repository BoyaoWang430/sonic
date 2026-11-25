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
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bytedance/sonic/internal/decoder/consts"
)

type typedNumTestStruct struct {
	Pad     string             `json:"pad"`
	I64     int64              `json:"i64"`
	I64Min  int64              `json:"i64_min"`
	I64Max  int64              `json:"i64_max"`
	I64Neg0 int64              `json:"i64_neg0"`
	I32     int32              `json:"i32"`
	I16     int16              `json:"i16"`
	I8      int8               `json:"i8"`
	U64     uint64             `json:"u64"`
	U64Max  uint64             `json:"u64_max"`
	U32     uint32             `json:"u32"`
	U16     uint16             `json:"u16"`
	U8      uint8              `json:"u8"`
	F64     float64            `json:"f64"`
	F64Zero float64            `json:"f64_zero"`
	F64Neg0 float64            `json:"f64_neg0"`
	F64Frac float64            `json:"f64_frac"`
	F64Long float64            `json:"f64_long"`
	F64Exp  float64            `json:"f64_exp"`
	F32     float32            `json:"f32"`
	F32Max  float32            `json:"f32_max"`
	F32Half float32            `json:"f32_half"`
	PI64    *int64             `json:"pi64"`
	PInt    *int               `json:"pint"`
	PI32    *int32             `json:"pi32"`
	PI16    *int16             `json:"pi16"`
	PI8     *int8              `json:"pi8"`
	PU64    *uint64            `json:"pu64"`
	PUint   *uint              `json:"puint"`
	PU32    *uint32            `json:"pu32"`
	PU16    *uint16            `json:"pu16"`
	PU8     *uint8             `json:"pu8"`
	PF64    *float64           `json:"pf64"`
	PF32    *float32           `json:"pf32"`
	Num     json.Number        `json:"num"`
	NumInf  json.Number        `json:"num_inf"`
	AnyInt  interface{}        `json:"any_int"`
	AnyFlt  interface{}        `json:"any_flt"`
	AnyInf  interface{}        `json:"any_inf"`
	AnyObj  interface{}        `json:"any_obj"`
	Raw     json.RawMessage    `json:"raw"`
	SliceI  []int64            `json:"slice_i"`
	SliceF  []float64          `json:"slice_f"`
	MapI    map[string]int64   `json:"map_i"`
	MapF    map[string]float64 `json:"map_f"`
}

func TestUseNumberTypedStructAndPlainParity(t *testing.T) {
	pad := strings.Repeat("x", 320)
	// Large payload (> 256B and > 24B/field) triggers stepSchemaFullEmit with TypedNumber;
	// small payload (< 256B) exercises stepPlainFullEmit + KRawNumber fast struct binding.
	largeJSON := `{"pad":"` + pad + `","i64":123456789012345,"i64_min":-9223372036854775808,"i64_max":9223372036854775807,"i64_neg0":-0,"i32":-2147483648,"i16":32767,"i8":-128,"u64":987654321098765,"u64_max":18446744073709551615,"u32":4294967295,"u16":65535,"u8":255,"f64":123.456,"f64_zero":0.0,"f64_neg0":-0,"f64_frac":0.95,"f64_long":0.0001234567890123456789,"f64_exp":1.5e-3,"f32":0.25,"f32_max":3.4028235e+38,"f32_half":1.00000011920928955078125,"pi64":-42,"pint":99,"pi32":-7,"pi16":12,"pi8":-3,"pu64":88,"puint":77,"pu32":66,"pu16":55,"pu8":44,"pf64":-0.5,"pf32":1.25,"num":1.000,"num_inf":1e400,"any_int":42,"any_flt":0.95,"any_inf":1e400,"any_obj":{"k":-0,"v":0.125,"arr":[1,2.5,1e400]},"raw":{"a":1.00},"slice_i":[0,-0,42,-9223372036854775808,9223372036854775807],"slice_f":[0,-0,0.95,1e5],"map_i":{"a":1,"b":-2},"map_f":{"x":0.5,"y":-0.0}}`

	var want typedNumTestStruct
	if err := stdDecodeUseNumberForTest(largeJSON, &want); err != nil {
		t.Fatalf("std decode large failed: %v", err)
	}

	optCases := []struct {
		name string
		opts uint64
	}{
		{"UseNumber", 1 << _F_use_number},
		{"UseNumber_CopyString", (1 << _F_use_number) | (1 << _F_copy_string)},
		{"UseNumber_CaseSensitive", (1 << _F_use_number) | uint64(consts.OptionCaseSensitive)},
	}
	for _, oc := range optCases {
		var got typedNumTestStruct
		if _, _, err := DecodeOne(largeJSON, oc.opts, &got); err != nil {
			t.Fatalf("%s decode large failed: %v", oc.name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s large mismatch:\n  got  = %+v\n  want = %+v", oc.name, got, want)
		}
		if !math.Signbit(got.F64Neg0) {
			t.Fatalf("%s expected -0.0 signbit for F64Neg0", oc.name)
		}
		if got.Num != json.Number("1.000") || got.NumInf != json.Number("1e400") {
			t.Fatalf("%s json.Number verbatim mismatch: num=%q num_inf=%q", oc.name, got.Num, got.NumInf)
		}
		if got.AnyInt != json.Number("42") || got.AnyFlt != json.Number("0.95") || got.AnyInf != json.Number("1e400") {
			t.Fatalf("%s any json.Number mismatch: %#v %#v %#v", oc.name, got.AnyInt, got.AnyFlt, got.AnyInf)
		}
	}

	type smallStruct struct {
		A   int64       `json:"a"`
		B   uint64      `json:"b"`
		C   float64     `json:"c"`
		D   float32     `json:"d"`
		PA  *int64      `json:"pa"`
		PC  *float64    `json:"pc"`
		Num json.Number `json:"num"`
		Any interface{} `json:"any"`
	}
	smallJSON := `{"a":-123456,"b":999,"c":0.95,"d":1.5,"pa":7,"pc":-0.25,"num":1.00,"any":1023}`
	var wantSmall smallStruct
	if err := stdDecodeUseNumberForTest(smallJSON, &wantSmall); err != nil {
		t.Fatalf("std decode small failed: %v", err)
	}
	for _, oc := range optCases {
		var got smallStruct
		if _, _, err := DecodeOne(smallJSON, oc.opts, &got); err != nil {
			t.Fatalf("%s decode small failed: %v", oc.name, err)
		}
		if !reflect.DeepEqual(got, wantSmall) {
			t.Fatalf("%s small mismatch:\n  got  = %+v\n  want = %+v", oc.name, got, wantSmall)
		}
	}
}

func TestUseNumberTypedMismatchAndOverflowParity(t *testing.T) {
	type mismatchStruct struct {
		Pad string  `json:"pad"`
		I64 int64   `json:"i64"`
		U64 uint64  `json:"u64"`
		F64 float64 `json:"f64"`
		F32 float32 `json:"f32"`
		Str string  `json:"str"`
		B   bool    `json:"b"`
	}
	bigPad := strings.Repeat("p", 320)
	cases := []struct {
		name string
		src  string
	}{
		{"small_f64_1e400", `{"f64":1e400}`},
		{"schema_f64_1e400", `{"pad":"` + bigPad + `","f64":1e400}`},
		{"schema_f32_1e400", `{"pad":"` + bigPad + `","f32":1e400}`},
		{"schema_i64_1e400", `{"pad":"` + bigPad + `","i64":1e400}`},
		{"schema_str_1e400", `{"pad":"` + bigPad + `","str":1e400}`},
		{"schema_bool_1e400", `{"pad":"` + bigPad + `","b":1e400}`},
		{"small_u64_neg0", `{"u64":-0}`},
		{"schema_u64_neg0", `{"pad":"` + bigPad + `","u64":-0}`},
		{"small_i64_1e2", `{"i64":1e2}`},
		{"schema_i64_1e2", `{"pad":"` + bigPad + `","i64":1e2}`},
		{"small_i64_1p0", `{"i64":1.0}`},
		{"schema_i64_1p0", `{"pad":"` + bigPad + `","i64":1.0}`},
		{"schema_i64_overflow", `{"pad":"` + bigPad + `","i64":9223372036854775808}`},
		{"schema_u64_overflow", `{"pad":"` + bigPad + `","u64":18446744073709551616}`},
	}

	for _, tc := range cases {
		var dstUse, dstNoUse, dstStd mismatchStruct
		_, _, errUse := DecodeOne(tc.src, 1<<_F_use_number, &dstUse)
		_, _, errNoUse := DecodeOne(tc.src, 0, &dstNoUse)
		errStd := stdDecodeUseNumberForTest(tc.src, &dstStd)
		if errStd == nil {
			t.Fatalf("%s: expected std error, got nil", tc.name)
		}
		if errUse == nil || errNoUse == nil {
			t.Fatalf("%s: expected mismatch error: errUse=%v errNoUse=%v", tc.name, errUse, errNoUse)
		}
		var mUse, mNoUse *MismatchTypeError
		var mUseVal, mNoUseVal MismatchTypeError
		if !errors.As(errUse, &mUse) && !errors.As(errUse, &mUseVal) {
			t.Fatalf("%s: expected MismatchTypeError under UseNumber, got %T (%v)", tc.name, errUse, errUse)
		}
		if !errors.As(errNoUse, &mNoUse) && !errors.As(errNoUse, &mNoUseVal) {
			t.Fatalf("%s: expected MismatchTypeError without UseNumber, got %T (%v)", tc.name, errNoUse, errNoUse)
		}
	}

	// Malformed number syntax errors under UseNumber must match exact SyntaxError.Pos,
	// SyntaxError.Msg (error reason), and consumed decoder cursor between plain/schema
	// struct paths and generic any decoding.
	extractSyntaxErr := func(err error) (int, string, bool) {
		var sp *SyntaxError
		if errors.As(err, &sp) {
			return sp.Pos, sp.Msg, true
		}
		var sv SyntaxError
		if errors.As(err, &sv) {
			return sv.Pos, sv.Msg, true
		}
		return 0, "", false
	}
	badNums := []string{
		`-`, `-a`, `01`, `-01`, `1.`, `0.`, `1.e2`, `0.e2`,
		`1.2.3`, `0.5.1`, `1.5+1`, `0.5-1`, `1e`, `1e+`, `1e+2.3`,
		`123+4`, `123-4`, `0+1`, `-0+1`, `123a`,
		`1.1234567890123456789+4`, `0.1234567890123456789+4`,
		`1e5+4`, `1e400+1`, `18446744073709551615+4`,
	}
	for _, bad := range badNums {
		for _, prefix := range []string{`{"f64":`, `{"pad":"` + bigPad + `","f64":`} {
			src := prefix + bad + `}`
			var d1 mismatchStruct
			var a1 interface{}
			_, c1, e1 := DecodeOne(src, 1<<_F_use_number, &d1)
			_, ca, ea := DecodeOne(src, 1<<_F_use_number, &a1)
			if e1 == nil || ea == nil {
				t.Fatalf("bad number %q (src=%q): expected syntax error, got struct=%v any=%v", bad, src, e1, ea)
			}
			p1, m1, ok1 := extractSyntaxErr(e1)
			pa, ma, oka := extractSyntaxErr(ea)
			if !ok1 || !oka {
				t.Fatalf("bad number %q: expected SyntaxError, got %T vs %T", bad, e1, ea)
			}
			if p1 != pa || m1 != ma || c1 != ca {
				t.Fatalf("bad number %q (UseNumber): mismatch struct=(pos=%d,cur=%d,msg=%q) vs any=(pos=%d,cur=%d,msg=%q) src=%q",
					bad, p1, c1, m1, pa, ca, ma, src)
			}
			// Also verify UseNumber=false struct vs any exact parity.
			var d2 mismatchStruct
			var a2 interface{}
			_, c2, e2 := DecodeOne(src, 0, &d2)
			_, ca2, ea2 := DecodeOne(src, 0, &a2)
			p2, m2, ok2 := extractSyntaxErr(e2)
			pa2, ma2, oka2 := extractSyntaxErr(ea2)
			if !ok2 || !oka2 || p2 != pa2 || m2 != ma2 || c2 != ca2 {
				t.Fatalf("bad number %q (NoUseNumber): mismatch struct=(pos=%d,cur=%d,msg=%q) vs any=(pos=%d,cur=%d,msg=%q)",
					bad, p2, c2, m2, pa2, ca2, ma2)
			}
		}
	}
}

// TestUseNumberStructAnyColdStartAndRace verifies that pre-boxed small
// json.Number tables are initialized at package init before any AsEface call,
// so struct fromObjFast decoding into nil any fields under UseNumber+!CopyString
// never observes nil or races across concurrent goroutines.
func TestUseNumberStructAnyColdStartAndRace(t *testing.T) {
	for i := 0; i < smallUintAnyLimit; i++ {
		want := json.Number(strconv.Itoa(i))
		if smallJSONNumberAny[i] != want {
			t.Fatalf("smallJSONNumberAny[%d] = %#v, want %#v", i, smallJSONNumberAny[i], want)
		}
	}
	for i := 1; i <= smallNegIntLimit; i++ {
		want := json.Number(strconv.Itoa(-i))
		if smallNegJSONNumberAny[i] != want {
			t.Fatalf("smallNegJSONNumberAny[%d] = %#v, want %#v", i, smallNegJSONNumberAny[i], want)
		}
	}

	type coldAnyStruct struct {
		A interface{} `json:"a"`
		B interface{} `json:"b"`
		C interface{} `json:"c"`
		D interface{} `json:"d"`
		E interface{} `json:"e"`
		F interface{} `json:"f"`
		G interface{} `json:"g"`
	}
	src := `{"a":42,"b":0,"c":-7,"d":1023,"e":-64,"f":1024,"g":-0}`

	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				var dst coldAnyStruct
				if _, _, err := DecodeOne(src, 1<<_F_use_number, &dst); err != nil {
					t.Errorf("DecodeOne failed: %v", err)
					return
				}
				if dst.A != json.Number("42") || dst.B != json.Number("0") ||
					dst.C != json.Number("-7") || dst.D != json.Number("1023") ||
					dst.E != json.Number("-64") || dst.F != json.Number("1024") ||
					dst.G != json.Number("-0") {
					t.Errorf("unexpected decoded values: %#v", dst)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
}
