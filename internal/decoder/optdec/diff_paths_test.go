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
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/ident"
	"github.com/bytedance/sonic/internal/native/number"
)

type diffChild struct {
	Code int32  `json:"code"`
	Tag  string `json:"tag"`
}

// diffFlatStruct is eligible for all 6 paths: router, runs, schema_proj, flat, giant, and chunked StreamDecoder (+ encoding/json).
type diffFlatStruct struct {
	ID      int64       `json:"id"`
	U64     uint64      `json:"u64"`
	F32     float32     `json:"f32"`
	F64     float64     `json:"f64"`
	Flag    bool        `json:"flag"`
	Name    string      `json:"name"`
	PtrInt  *int64      `json:"ptr_int"`
	PtrInt2 *int64      `json:"ptr_int2"`
	Nums    []int       `json:"nums"`
	Nums2   []int       `json:"nums2"`
	Tags    []string    `json:"tags"`
	Fixed   [2]int      `json:"fixed"`
	Kids    []diffChild `json:"kids"`
	Child   diffChild   `json:"child"`
}

// diffRichStruct includes map and ,string fields for router, runs, schema_proj, giant, and chunked StreamDecoder.
type diffRichStruct struct {
	ID     int64             `json:"id"`
	F32    float32           `json:"f32"`
	Name   string            `json:"name"`
	Meta   map[string]string `json:"meta"`
	Child  diffChild         `json:"child"`
	StrInt int               `json:"str_int,string"`
}

type diffRawItem struct {
	Value any `json:"value"`
}

type diffRawEnvelope struct {
	ID    int             `json:"id"`
	Raw   json.RawMessage `json:"raw"`
	Note  string          `json:"note"`
	Val   any             `json:"val"`
	Items []diffRawItem   `json:"items"`
	Anys  []any           `json:"anys"`
}

var (
	diffStatsExecs          atomic.Uint64
	diffStatsSchemaHit      atomic.Uint64
	diffStatsSchemaFallback atomic.Uint64
)

func resetDiffPathStats() {
	diffStatsExecs.Store(0)
	diffStatsSchemaHit.Store(0)
	diffStatsSchemaFallback.Store(0)
}

// hasStdPre127StrconvFloatBug reports whether s contains a >800-byte JSON number
// token on which pre-Go 1.27 stdlib strconv.ParseFloat itself produces a
// mathematically wrong result (e.g. F64-LONG-OVF). Internal Sonic path
// comparisons always run without exemption; only the stdlib oracle comparison
// on pre-Go 1.27 toolchains checks this exact stdlib defect.
func hasStdPre127StrconvFloatBug(s string) bool {
	if diffStdUsesJSONV2 || len(s) <= 800 {
		return false
	}
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			continue
		}
		if c == '-' || (c >= '0' && c <= '9') {
			end, perr := number.Scan(s, i)
			if perr == 0 && end-i > 800 {
				tok := s[i:end]
				stdF64, stdErr64 := strconv.ParseFloat(tok, 64)
				exactF64, exactOK64 := number.ParseValidJSONFloat64(tok)
				if (stdErr64 == nil) != exactOK64 || (exactOK64 && math.Float64bits(stdF64) != math.Float64bits(exactF64)) {
					return true
				}
				stdF32, stdErr32 := strconv.ParseFloat(tok, 32)
				exactF32, exactOK32 := number.ParseValidJSONFloat32(tok)
				if (stdErr32 == nil) != exactOK32 || (exactOK32 && math.Float32bits(float32(stdF32)) != math.Float32bits(exactF32)) {
					return true
				}
			}
			if end > i {
				i = end - 1
			}
		}
	}
	return false
}

const (
	diffStdOpts        uint64 = (1 << _F_copy_string) | (1 << _F_validate_string)
	diffStdDisUnknown  uint64 = diffStdOpts | (1 << _F_disable_unknown)
	diffStrictURCOpts  uint64 = diffStdOpts | (1 << _F_disable_urc)
	diffCaseSensOpts   uint64 = diffStdOpts | uint64(consts.OptionCaseSensitive)
	diffFastCopyOpts   uint64 = 1 << _F_copy_string
	diffUseNumberOpts  uint64 = diffStdOpts | (1 << _F_use_number)
	diffUseInt64Opts   uint64 = diffStdOpts | (1 << _F_use_int64)
	diffNoValValidOpts uint64 = (1 << _F_copy_string) | (1 << _F_no_validate_json)
)

func checkSingleDocEnd(s string, endPos int, err error) error {
	if err != nil {
		return err
	}
	if hasMoreJSONTokens(s, endPos) {
		return SyntaxError{Pos: endPos, Src: s, Msg: "invalid trailing characters"}
	}
	return nil
}

func decodeViaRunsPosWithInit[T any](s string, f uint64, initFn func(*T)) (T, int, error) {
	var dst T
	if initFn != nil {
		initFn(&dst)
	}
	etp, vp, dec, _, err := unpackAndCompileRoot(&dst)
	if err != nil {
		return dst, 0, err
	}
	p := newParserImpl(s, 0, f)
	defer p.free()
	doc, ecode := p.scanDocument(f, etp, nil)
	utf8Inv := doc.Utf8Inv || p.Utf8Inv
	if ecode != SONIC_OK {
		err = p.fixError(ecode)
	} else {
		ctx := p.initDecodeContext(doc, f, etp)
		err = dec.FromDom(vp, ctx.Root(), ctx)
	}
	endPos := p.Pos()
	if utf8Inv {
		endPos = mapRepairedOffsetToOrig(s, endPos)
	}
	if endPos > len(s) {
		endPos = len(s)
	}
	return dst, endPos, checkSingleDocEnd(s, endPos, fix_error_orig(s, 0, utf8Inv, err))
}

func decodeViaRunsWithInit[T any](s string, f uint64, initFn func(*T)) (T, error) {
	v, _, err := decodeViaRunsPosWithInit[T](s, f, initFn)
	return v, err
}

func decodeViaScalarRunsPosWithInit[T any](s string, f uint64, initFn func(*T)) (T, int, error) {
	var dst T
	if initFn != nil {
		initFn(&dst)
	}
	etp, vp, dec, _, err := unpackAndCompileRoot(&dst)
	if err != nil {
		return dst, 0, err
	}
	p := newParserImpl(s, 0, f)
	defer p.free()
	p.isEface = etp == anyType || etp == mapEfaceType || etp == sliceEfaceType
	p.strictURC = (f & (1 << _F_disable_urc)) != 0
	p.keepUseNumber = (f & (1 << _F_use_number)) != 0
	p.deferFloatOverflow = !p.keepUseNumber
	old := p.options
	if !p.isEface && !p.keepUseNumber {
		p.options &^= 1 << _F_use_number
	}
	ecode := parseRunsGrowingMode(p, true)
	p.options = old
	utf8Inv := p.Utf8Inv
	if ecode != SONIC_OK {
		err = p.fixError(ecode)
	} else {
		ctx := p.initDecodeContext(p.snapshotDocument(), f, etp)
		err = dec.FromDom(vp, ctx.Root(), ctx)
	}
	endPos := p.Pos()
	if utf8Inv {
		endPos = mapRepairedOffsetToOrig(s, endPos)
	}
	if endPos > len(s) {
		endPos = len(s)
	}
	return dst, endPos, checkSingleDocEnd(s, endPos, fix_error_orig(s, 0, utf8Inv, err))
}

func decodeViaScalarRunsWithInit[T any](s string, f uint64, initFn func(*T)) (T, error) {
	v, _, err := decodeViaScalarRunsPosWithInit[T](s, f, initFn)
	return v, err
}

func classifyContractErr(err error) string {
	if err == nil {
		return "nil"
	}
	switch err.(type) {
	case SyntaxError, *SyntaxError:
		return "syntax"
	case *MismatchTypeError, MismatchTypeError:
		return "mismatch"
	default:
		return "other"
	}
}

func contractSyntaxDetail(err error) (int, string, bool) {
	switch e := err.(type) {
	case SyntaxError:
		return e.Pos, e.Message(), true
	case *SyntaxError:
		if e != nil {
			return e.Pos, e.Message(), true
		}
	}
	return -1, "", false
}

func contractMismatchDetail(err error) (int, reflect.Type, bool) {
	switch e := err.(type) {
	case MismatchTypeError:
		return e.Pos, e.Type, true
	case *MismatchTypeError:
		if e != nil {
			return e.Pos, e.Type, true
		}
	}
	return -1, nil, false
}

func assertSonicErrorDetailParity(t *testing.T, s string, f uint64, runsErr error, peers map[string]error) {
	t.Helper()
	if wantPos, wantMsg, ok := contractSyntaxDetail(runsErr); ok {
		for name, perr := range peers {
			gotPos, gotMsg, gok := contractSyntaxDetail(perr)
			if !gok || gotPos != wantPos || gotMsg != wantMsg {
				t.Fatalf("SyntaxError detail mismatch (%s vs runs, f=0x%x) on %q:\n  %s=%T (pos=%d, msg=%q, err=%v)\n  runs=%T (pos=%d, msg=%q, err=%v)",
					name, f, s, name, perr, gotPos, gotMsg, perr, runsErr, wantPos, wantMsg, runsErr)
			}
		}
		return
	}
	if wantPos, wantTyp, ok := contractMismatchDetail(runsErr); ok {
		for name, perr := range peers {
			gotPos, gotTyp, gok := contractMismatchDetail(perr)
			if !gok || gotPos != wantPos || gotTyp != wantTyp {
				t.Fatalf("MismatchTypeError detail mismatch (%s vs runs, f=0x%x) on %q:\n  %s=%T (pos=%d, typ=%v, err=%v)\n  runs=%T (pos=%d, typ=%v, err=%v)",
					name, f, s, name, perr, gotPos, gotTyp, perr, runsErr, wantPos, wantTyp, runsErr)
			}
		}
	}
}

func decodeViaSchemaOrFullPosWithInit[T any](s string, f uint64, initFn func(*T)) (T, int, bool, error) {
	var dst T
	if initFn != nil {
		initFn(&dst)
	}
	etp, vp, dec, proj, err := unpackAndCompileRoot(&dst)
	if err != nil {
		return dst, 0, false, err
	}
	if proj == nil || proj.kind == projFull || proj.kind == projScalar || proj.kind == projBytes {
		v, endPos, rerr := decodeViaRunsPosWithInit[T](s, f, initFn)
		return v, endPos, false, rerr
	}
	p := newParserLazy(s, 0, f)
	defer p.free()
	p.isEface = etp == anyType || etp == mapEfaceType || etp == sliceEfaceType
	p.strictURC = (f & (1 << _F_disable_urc)) != 0
	p.keepUseNumber = (f & (1 << _F_use_number)) != 0
	p.deferFloatOverflow = !p.keepUseNumber
	outcome, code := parseSchemaRoot(p, proj)
	usedSchema := outcome != schemaOutcomeNotEligible
	if outcome == schemaOutcomeNotEligible {
		code = parseImpl(p)
	}
	if code != 0 {
		utf8Inv := p.Utf8Inv
		endPos := p.Pos()
		if utf8Inv {
			endPos = mapRepairedOffsetToOrig(s, endPos)
		}
		return dst, endPos, usedSchema, fix_error_orig(s, 0, utf8Inv, p.fixError(code))
	}
	utf8Inv := p.Utf8Inv
	ctx := p.initDecodeContext(p.snapshotDocument(), f, etp)
	err = dec.FromDom(vp, ctx.Root(), ctx)
	endPos := p.Pos()
	if utf8Inv {
		endPos = mapRepairedOffsetToOrig(s, endPos)
	}
	if endPos > len(s) {
		endPos = len(s)
	}
	return dst, endPos, usedSchema, checkSingleDocEnd(s, endPos, fix_error_orig(s, 0, utf8Inv, err))
}

func decodeViaSchemaOrFullWithInit[T any](s string, f uint64, initFn func(*T)) (T, bool, error) {
	v, _, ok, err := decodeViaSchemaOrFullPosWithInit[T](s, f, initFn)
	return v, ok, err
}

func decodeViaGiantPosWithInit[T any](s string, f uint64, initFn func(*T)) (T, int, error) {
	var dst T
	if initFn != nil {
		initFn(&dst)
	}
	etp, vp, dec, _, err := unpackAndCompileRoot(&dst)
	if err != nil {
		return dst, 0, err
	}
	sOut, endPos, err := decodeReadOnlyOneCompiled(s, f, etp, vp, dec)
	return dst, endPos, checkSingleDocEnd(sOut, endPos, err)
}

func decodeViaGiantWithInit[T any](s string, f uint64, initFn func(*T)) (T, error) {
	v, _, err := decodeViaGiantPosWithInit[T](s, f, initFn)
	return v, err
}

type chunkedStreamState struct {
	src       string
	srcOff    int
	scanned   int
	chunkSize int
	buf       []byte
	scanp     int
}

func chunkedStreamRefill(arg unsafe.Pointer, compactTo int) (bool, error) {
	st := (*chunkedStreamState)(arg)
	if compactTo > 0 {
		st.scanned += compactTo
		n := copy(st.buf, st.buf[compactTo:])
		st.buf = st.buf[:n]
		st.scanp = 0
	}
	if st.srcOff >= len(st.src) {
		return true, nil
	}
	end := st.srcOff + st.chunkSize
	if end > len(st.src) {
		end = len(st.src)
	}
	st.buf = append(st.buf, st.src[st.srcOff:end]...)
	st.srcOff = end
	return st.srcOff >= len(st.src), nil
}

func decodeViaChunkedStreamPosWithInit[T any](s string, f uint64, chunkSize int, initFn func(*T)) (T, int, error) {
	var dst T
	if initFn != nil {
		initFn(&dst)
	}
	if chunkSize <= 0 {
		chunkSize = 7
	}
	st := chunkedStreamState{
		src:       s,
		chunkSize: chunkSize,
	}
	var sess *StreamSession
	defer func() {
		if sess != nil {
			FreeStreamSession(sess)
		}
	}()
	err := DecodeStream(&st.buf, &st.scanp, f, &dst, &sess, unsafe.Pointer(&st), chunkedStreamRefill)
	consumed := st.scanned + st.scanp
	if consumed > len(s) {
		consumed = len(s)
	}
	if err != nil {
		return dst, consumed, err
	}
	return dst, consumed, checkSingleDocEnd(s, consumed, nil)
}

func decodeViaChunkedStreamWithInit[T any](s string, f uint64, chunkSize int, initFn func(*T)) (T, error) {
	v, _, err := decodeViaChunkedStreamPosWithInit[T](s, f, chunkSize, initFn)
	return v, err
}

func decodeViaStdGenericWithOptsAndInit[T any](s string, f uint64, initFn func(*T)) (T, error) {
	var dst T
	if initFn != nil {
		initFn(&dst)
	}
	disUnknown := (f & (1 << _F_disable_unknown)) != 0
	useNum := (f & (1 << _F_use_number)) != 0
	if !disUnknown && !useNum {
		err := json.Unmarshal([]byte(s), &dst)
		return dst, err
	}
	dec := json.NewDecoder(strings.NewReader(s))
	if disUnknown {
		dec.DisallowUnknownFields()
	}
	if useNum {
		dec.UseNumber()
	}
	if err := dec.Decode(&dst); err != nil {
		return dst, err
	}
	var extra any
	if dec.More() || dec.Decode(&extra) == nil || !json.Valid([]byte(s)) {
		return dst, &json.SyntaxError{Offset: int64(len(s))}
	}
	return dst, nil
}

func checkGenericSixWayWithInit[T any](t *testing.T, s string, f uint64, checkStd bool, _ bool, initFn func(*T)) {
	t.Helper()
	diffStatsExecs.Add(1)

	origBuf := []byte(s)
	callerBuf := append([]byte(nil), origBuf...)
	sCaller := unsafe.String(unsafe.SliceData(callerBuf), len(callerBuf))

	var gotRouter T
	if initFn != nil {
		initFn(&gotRouter)
	}
	sRouter, endRouter, errRouter := DecodeOne(sCaller, f, &gotRouter)
	errRouter = checkSingleDocEnd(sRouter, endRouter, errRouter)

	gotRuns, endRuns, errRuns := decodeViaRunsPosWithInit[T](sCaller, f, initFn)
	gotScalar, endScalar, errScalar := decodeViaScalarRunsPosWithInit[T](sCaller, f, initFn)
	gotSchema, endSchema, usedSchema, errSchema := decodeViaSchemaOrFullPosWithInit[T](sCaller, f, initFn)
	if usedSchema {
		diffStatsSchemaHit.Add(1)
	} else {
		diffStatsSchemaFallback.Add(1)
	}
	gotGiant, endGiant, errGiant := decodeViaGiantPosWithInit[T](sCaller, f, initFn)
	gotStream, endStream, errStream := decodeViaChunkedStreamPosWithInit[T](sCaller, f, 7, initFn)

	if !bytes.Equal(callerBuf, origBuf) {
		t.Fatalf("%T mutated caller input buffer (f=0x%x) on %q", gotRuns, f, s)
	}

	noValSkip := (f & (1 << _F_no_validate_json)) != 0
	if !noValSkip {
		runsKind := classifyContractErr(errRuns)
		if classifyContractErr(errRouter) != runsKind {
			t.Fatalf("%T router vs runs err kind mismatch (f=0x%x) on %q:\n  router=%T (%v)\n  runs=%T (%v)", gotRuns, f, s, errRouter, errRouter, errRuns, errRuns)
		}
		if classifyContractErr(errScalar) != runsKind {
			t.Fatalf("%T scalar vs runs err kind mismatch (f=0x%x) on %q:\n  scalar=%T (%v)\n  runs=%T (%v)", gotRuns, f, s, errScalar, errScalar, errRuns, errRuns)
		}
		if classifyContractErr(errSchema) != runsKind {
			t.Fatalf("%T schema vs runs err kind mismatch (f=0x%x) on %q:\n  schema=%T (%v)\n  runs=%T (%v)", gotRuns, f, s, errSchema, errSchema, errRuns, errRuns)
		}
		if classifyContractErr(errGiant) != runsKind {
			t.Fatalf("%T giant vs runs err kind mismatch (f=0x%x) on %q:\n  giant=%T (%v)\n  runs=%T (%v)", gotRuns, f, s, errGiant, errGiant, errRuns, errRuns)
		}
		if classifyContractErr(errStream) != runsKind {
			t.Fatalf("%T stream vs runs err kind mismatch (f=0x%x) on %q:\n  stream=%T (%v)\n  runs=%T (%v)", gotRuns, f, s, errStream, errStream, errRuns, errRuns)
		}
		if runsKind == "syntax" || runsKind == "mismatch" {
			assertSonicErrorDetailParity(t, s, f, errRuns, map[string]error{
				"router": errRouter,
				"scalar": errScalar,
				"schema": errSchema,
				"giant":  errGiant,
				"stream": errStream,
			})
		}
		if errRuns == nil && utf8.ValidString(s) {
			if endRouter != endRuns || endScalar != endRuns || endSchema != endRuns || endGiant != endRuns || endStream != endRuns {
				t.Fatalf("%T consumed offset mismatch (f=0x%x) on %q: router=%d runs=%d scalar=%d schema=%d giant=%d stream=%d",
					gotRuns, f, s, endRouter, endRuns, endScalar, endSchema, endGiant, endStream)
			}
		}

		// Contract Dimension 4: Verify destination target state on BOTH success and error!
		if !reflect.DeepEqual(gotRouter, gotRuns) {
			t.Fatalf("%T router vs runs target state mismatch (err=%v, f=0x%x) on %q:\n  router=%#v\n  runs=%#v", gotRuns, errRuns, f, s, gotRouter, gotRuns)
		}
		if !reflect.DeepEqual(gotScalar, gotRuns) {
			t.Fatalf("%T scalar vs runs target state mismatch (err=%v, f=0x%x) on %q:\n  scalar=%#v\n  runs=%#v", gotRuns, errRuns, f, s, gotScalar, gotRuns)
		}
		if !reflect.DeepEqual(gotSchema, gotRuns) {
			t.Fatalf("%T schema vs runs target state mismatch (err=%v, f=0x%x) on %q:\n  schema=%#v\n  runs=%#v", gotRuns, errRuns, f, s, gotSchema, gotRuns)
		}
		if !reflect.DeepEqual(gotGiant, gotRuns) {
			t.Fatalf("%T giant vs runs target state mismatch (err=%v, f=0x%x) on %q:\n  giant=%#v\n  runs=%#v", gotRuns, errRuns, f, s, gotGiant, gotRuns)
		}
		if !reflect.DeepEqual(gotStream, gotRuns) {
			t.Fatalf("%T stream vs runs target state mismatch (err=%v, f=0x%x) on %q:\n  stream=%#v\n  runs=%#v", gotRuns, errRuns, f, s, gotStream, gotRuns)
		}
	} else if errRuns == nil {
		if errRouter != nil || errScalar != nil || errSchema != nil || errGiant != nil || errStream != nil {
			t.Fatalf("%T NoValidateJSON failed on input accepted by runs (f=0x%x) on %q: router=%v scalar=%v schema=%v giant=%v stream=%v",
				gotRuns, f, s, errRouter, errScalar, errSchema, errGiant, errStream)
		}
		if !reflect.DeepEqual(gotRouter, gotRuns) || !reflect.DeepEqual(gotScalar, gotRuns) ||
			!reflect.DeepEqual(gotSchema, gotRuns) || !reflect.DeepEqual(gotGiant, gotRuns) || !reflect.DeepEqual(gotStream, gotRuns) {
			t.Fatalf("%T NoValidateJSON value mismatch (f=0x%x) on %q", gotRuns, f, s)
		}
	}

	if checkStd && utf8.ValidString(s) {
		if hasStdPre127StrconvFloatBug(s) {
			return
		}
		gotStd, errStd := decodeViaStdGenericWithOptsAndInit[T](s, f, initFn)
		if (errRuns != nil) != (errStd != nil) {
			t.Fatalf("%T runs vs encoding/json err mismatch (f=0x%x) on %q:\n  runs=%v\n  std=%v", gotRuns, f, s, errRuns, errStd)
		}
		if errStd == nil && !reflect.DeepEqual(gotRuns, gotStd) {
			t.Fatalf("%T runs vs encoding/json value mismatch (f=0x%x) on %q:\n  runs=%#v\n  std=%#v", gotRuns, f, s, gotRuns, gotStd)
		}
	}
}

func checkGenericSixWay[T any](t *testing.T, s string, f uint64, checkStd bool, withFlat bool) {
	t.Helper()
	checkGenericSixWayWithInit[T](t, s, f, checkStd, withFlat, nil)
}

func flatInitializerModes() []func(*diffFlatStruct) {
	return []func(*diffFlatStruct){
		// Mode 0: Disjoint pre-populated pointers and reusable slices (hits Flat Reuse fast path).
		func(d *diffFlatStruct) {
			a, b := int64(111), int64(-222)
			d.ID = -999
			d.Name = "dirty-name"
			d.Flag = true
			d.PtrInt = &a
			d.PtrInt2 = &b
			s1 := make([]int, 2, 4)
			s1[0], s1[1] = 88, 99
			s2 := make([]int, 1, 4)
			s2[0] = 77
			d.Nums = s1
			d.Nums2 = s2
			d.Fixed = [2]int{44, 55}
			d.Kids = []diffChild{{Code: 9, Tag: "old"}}
			d.Child = diffChild{Code: 8, Tag: "old-child"}
		},
		// Mode 1: Shared pointer alias (PtrInt and PtrInt2 point to the exact same int64).
		func(d *diffFlatStruct) {
			shared := int64(555)
			d.PtrInt = &shared
			d.PtrInt2 = &shared
		},
		// Mode 2: Self-referential pointer (PtrInt points directly to d.ID inside d).
		func(d *diffFlatStruct) {
			d.ID = 333
			d.PtrInt = &d.ID
		},
		// Mode 3: Overlapping slice backing arrays (Nums and Nums2 share the same backing array).
		func(d *diffFlatStruct) {
			backing := []int{10, 20, 30, 40}
			d.Nums = backing[:2:4]
			d.Nums2 = backing[:2:4]
		},
	}
}

func checkAliasedSideEffects(t *testing.T, s string) {
	t.Helper()
	// Independent identical-topology external pointer & slice backing graph per path.
	sRouterV, sRunsV, sScalarV, sSchemaV, sGiantV, sStreamV := int64(77), int64(77), int64(77), int64(77), int64(77), int64(77)
	bRouter := [4]int{10, 20, 30, 40}
	bRuns := [4]int{10, 20, 30, 40}
	bScalar := [4]int{10, 20, 30, 40}
	bSchema := [4]int{10, 20, 30, 40}
	bGiant := [4]int{10, 20, 30, 40}
	bStream := [4]int{10, 20, 30, 40}

	makeInit := func(ptr *int64, back *[4]int) func(*diffFlatStruct) {
		return func(d *diffFlatStruct) {
			d.PtrInt, d.PtrInt2 = ptr, ptr
			d.Nums = back[:2:4]
			d.Nums2 = back[:2:4]
		}
	}

	var dRouter diffFlatStruct
	makeInit(&sRouterV, &bRouter)(&dRouter)
	sR, endR, errRouter := DecodeOne(s, diffStdOpts, &dRouter)
	errRouter = checkSingleDocEnd(sR, endR, errRouter)

	dRuns, errRuns := decodeViaRunsWithInit[diffFlatStruct](s, diffStdOpts, makeInit(&sRunsV, &bRuns))
	dScalar, errScalar := decodeViaScalarRunsWithInit[diffFlatStruct](s, diffStdOpts, makeInit(&sScalarV, &bScalar))
	dSchema, _, errSchema := decodeViaSchemaOrFullWithInit[diffFlatStruct](s, diffStdOpts, makeInit(&sSchemaV, &bSchema))
	dGiant, errGiant := decodeViaGiantWithInit[diffFlatStruct](s, diffStdOpts, makeInit(&sGiantV, &bGiant))
	dStream, errStream := decodeViaChunkedStreamWithInit[diffFlatStruct](s, diffStdOpts, 7, makeInit(&sStreamV, &bStream))

	runsKind := classifyContractErr(errRuns)
	for name, err := range map[string]error{
		"router": errRouter, "scalar": errScalar, "schema": errSchema, "giant": errGiant, "stream": errStream,
	} {
		if classifyContractErr(err) != runsKind {
			t.Fatalf("alias %s vs runs err kind mismatch on %q: %s=%v runs=%v", name, s, name, err, errRuns)
		}
	}
	if runsKind == "syntax" || runsKind == "mismatch" {
		assertSonicErrorDetailParity(t, s, diffStdOpts, errRuns, map[string]error{
			"router": errRouter, "scalar": errScalar, "schema": errSchema, "giant": errGiant, "stream": errStream,
		})
	}
	// Verify BOTH destination structs AND external aliased storage on success AND failure.
	if sRouterV != sRunsV || sScalarV != sRunsV || sSchemaV != sRunsV || sGiantV != sRunsV || sStreamV != sRunsV {
		t.Fatalf("shared pointer value diverged (err=%v) on %q: router=%d runs=%d scalar=%d schema=%d giant=%d stream=%d",
			errRuns, s, sRouterV, sRunsV, sScalarV, sSchemaV, sGiantV, sStreamV)
	}
	if bRouter != bRuns || bScalar != bRuns || bSchema != bRuns || bGiant != bRuns || bStream != bRuns {
		t.Fatalf("shared slice backing diverged (err=%v) on %q: router=%v runs=%v scalar=%v schema=%v giant=%v stream=%v",
			errRuns, s, bRouter, bRuns, bScalar, bSchema, bGiant, bStream)
	}
	if !reflect.DeepEqual(dRouter, dRuns) || !reflect.DeepEqual(dScalar, dRuns) ||
		!reflect.DeepEqual(dSchema, dRuns) || !reflect.DeepEqual(dGiant, dRuns) || !reflect.DeepEqual(dStream, dRuns) {
		t.Fatalf("aliased target state diverged (err=%v) on %q", errRuns, s)
	}

	if utf8.ValidString(s) && !hasStdPre127StrconvFloatBug(s) {
		sStdV := int64(77)
		bStd := [4]int{10, 20, 30, 40}
		var dStd diffFlatStruct
		makeInit(&sStdV, &bStd)(&dStd)
		errStd := json.Unmarshal([]byte(s), &dStd)
		if (errRuns != nil) != (errStd != nil) {
			t.Fatalf("alias runs vs std err mismatch on %q: runs=%v std=%v", s, errRuns, errStd)
		}
		if errStd == nil && (sRunsV != sStdV || bRuns != bStd || !reflect.DeepEqual(dRuns, dStd)) {
			t.Fatalf("alias runs vs std state mismatch on %q: runs=(%d,%v) std=(%d,%v)", s, sRunsV, bRuns, sStdV, bStd)
		}
	}
}

func checkAnyFiveWay(t *testing.T, s string, f uint64) {
	t.Helper()
	var gotRouter any
	sRouter, endRouter, errRouter := DecodeOne(s, f, &gotRouter)
	errRouter = checkSingleDocEnd(sRouter, endRouter, errRouter)
	gotRuns, endRuns, errRuns := decodeViaRunsPosWithInit[any](s, f, nil)
	gotScalar, endScalar, errScalar := decodeViaScalarRunsPosWithInit[any](s, f, nil)
	gotGiant, endGiant, errGiant := decodeViaGiantPosWithInit[any](s, f, nil)
	gotStream, endStream, errStream := decodeViaChunkedStreamPosWithInit[any](s, f, 7, nil)

	runsKind := classifyContractErr(errRuns)
	if classifyContractErr(errRouter) != runsKind || classifyContractErr(errScalar) != runsKind ||
		classifyContractErr(errGiant) != runsKind || classifyContractErr(errStream) != runsKind {
		t.Fatalf("any err mismatch (f=0x%x) on %q: router=%v runs=%v scalar=%v giant=%v stream=%v",
			f, s, errRouter, errRuns, errScalar, errGiant, errStream)
	}
	if runsKind == "syntax" || runsKind == "mismatch" {
		assertSonicErrorDetailParity(t, s, f, errRuns, map[string]error{
			"router": errRouter, "scalar": errScalar, "giant": errGiant, "stream": errStream,
		})
	}
	if errRuns == nil && utf8.ValidString(s) {
		if endRouter != endRuns || endScalar != endRuns || endGiant != endRuns || endStream != endRuns {
			t.Fatalf("any consumed mismatch (f=0x%x) on %q: router=%d runs=%d scalar=%d giant=%d stream=%d",
				f, s, endRouter, endRuns, endScalar, endGiant, endStream)
		}
	}
	if !reflect.DeepEqual(gotRouter, gotRuns) || !reflect.DeepEqual(gotScalar, gotRuns) ||
		!reflect.DeepEqual(gotGiant, gotRuns) || !reflect.DeepEqual(gotStream, gotRuns) {
		t.Fatalf("any state mismatch (err=%v, f=0x%x) on %q:\n  router=%#v\n  runs=%#v\n  scalar=%#v\n  giant=%#v\n  stream=%#v",
			errRuns, f, s, gotRouter, gotRuns, gotScalar, gotGiant, gotStream)
	}
	if (f == diffStdOpts || f == diffUseNumberOpts) && utf8.ValidString(s) {
		if hasStdPre127StrconvFloatBug(s) {
			return
		}
		gotStd, errStd := decodeViaStdGenericWithOptsAndInit[any](s, f, nil)
		if (errRuns != nil) != (errStd != nil) {
			t.Fatalf("any vs std err mismatch (f=0x%x) on %q: runs=%v std=%v", f, s, errRuns, errStd)
		}
		if errStd == nil && !reflect.DeepEqual(gotRuns, gotStd) {
			t.Fatalf("any vs std value mismatch (f=0x%x) on %q:\n  runs=%#v\n  std=%#v", f, s, gotRuns, gotStd)
		}
	}
}

func rawEnvelopeInitializerModes() []func(*diffRawEnvelope) {
	return []func(*diffRawEnvelope){
		nil,
		// Pre-populated top-level RawMessage & fields to verify residual preservation on error.
		func(d *diffRawEnvelope) {
			d.ID = -42
			d.Note = "keep-note"
			d.Raw = json.RawMessage(`{"keep":77}`)
		},
		// Dynamic *json.RawMessage inside interface{} field Val.
		func(d *diffRawEnvelope) {
			raw := json.RawMessage(`{"prev":1}`)
			d.Val = &raw
		},
		// Dynamic *json.RawMessage inside slice element struct interface{} (Review 2.1).
		func(d *diffRawEnvelope) {
			raw := json.RawMessage(`[9]`)
			d.Items = []diffRawItem{{Value: &raw}}
		},
		// Dynamic *json.RawMessage inside []any (len=1, cap=1).
		func(d *diffRawEnvelope) {
			raw := json.RawMessage(`"old"`)
			d.Anys = []any{&raw}
		},
		// Dynamic *json.RawMessage inside resliced ([]any{&raw})[:0] (len=0, cap=1).
		func(d *diffRawEnvelope) {
			raw := json.RawMessage(`true`)
			d.Anys = ([]any{&raw})[:0]
		},
	}
}

func equalRawEnvelope(a, b diffRawEnvelope) bool {
	return a.ID == b.ID &&
		a.Note == b.Note &&
		bytes.Equal(a.Raw, b.Raw) &&
		reflect.DeepEqual(a.Val, b.Val) &&
		reflect.DeepEqual(a.Items, b.Items) &&
		reflect.DeepEqual(a.Anys, b.Anys)
}

func checkRawEnvelopeFiveWayWithInit(t *testing.T, s string, f uint64, checkStd bool, initFn func(*diffRawEnvelope)) {
	t.Helper()
	diffStatsExecs.Add(1)
	var gotRouter diffRawEnvelope
	if initFn != nil {
		initFn(&gotRouter)
	}
	sRouter, endRouter, errRouter := DecodeOne(s, f, &gotRouter)
	errRouter = checkSingleDocEnd(sRouter, endRouter, errRouter)
	gotRuns, endRuns, errRuns := decodeViaRunsPosWithInit[diffRawEnvelope](s, f, initFn)
	gotScalar, endScalar, errScalar := decodeViaScalarRunsPosWithInit[diffRawEnvelope](s, f, initFn)
	gotSchema, endSchema, usedSchema, errSchema := decodeViaSchemaOrFullPosWithInit[diffRawEnvelope](s, f, initFn)
	if usedSchema {
		diffStatsSchemaHit.Add(1)
	} else {
		diffStatsSchemaFallback.Add(1)
	}
	gotGiant, endGiant, errGiant := decodeViaGiantPosWithInit[diffRawEnvelope](s, f, initFn)
	gotStream, endStream, errStream := decodeViaChunkedStreamPosWithInit[diffRawEnvelope](s, f, 7, initFn)

	noValSkip := (f & (1 << _F_no_validate_json)) != 0
	if !noValSkip {
		runsKind := classifyContractErr(errRuns)
		if classifyContractErr(errRouter) != runsKind ||
			classifyContractErr(errScalar) != runsKind ||
			classifyContractErr(errSchema) != runsKind ||
			classifyContractErr(errGiant) != runsKind ||
			classifyContractErr(errStream) != runsKind {
			t.Fatalf("raw envelope err mismatch (f=0x%x) on %q: router=%v runs=%v scalar=%v schema=%v giant=%v stream=%v",
				f, s, errRouter, errRuns, errScalar, errSchema, errGiant, errStream)
		}
		if runsKind == "syntax" || runsKind == "mismatch" {
			assertSonicErrorDetailParity(t, s, f, errRuns, map[string]error{
				"router": errRouter, "scalar": errScalar, "schema": errSchema, "giant": errGiant, "stream": errStream,
			})
		}
		if errRuns == nil && utf8.ValidString(s) {
			if endRouter != endRuns || endScalar != endRuns || endSchema != endRuns || endGiant != endRuns || endStream != endRuns {
				t.Fatalf("raw envelope consumed mismatch (f=0x%x) on %q: router=%d runs=%d scalar=%d schema=%d giant=%d stream=%d",
					f, s, endRouter, endRuns, endScalar, endSchema, endGiant, endStream)
			}
		}
		// Compare residual destination state on BOTH success and failure.
		for name, g := range map[string]diffRawEnvelope{
			"router": gotRouter,
			"scalar": gotScalar,
			"schema": gotSchema,
			"giant":  gotGiant,
			"stream": gotStream,
		} {
			if !equalRawEnvelope(g, gotRuns) {
				t.Fatalf("raw %s vs runs state mismatch (err=%v) on %q:\n  got=%#v\n  runs=%#v", name, errRuns, s, g, gotRuns)
			}
		}
	} else if errRuns == nil {
		if errRouter != nil || errScalar != nil || errSchema != nil || errGiant != nil || errStream != nil {
			t.Fatalf("raw envelope NoValidateJSON failed on valid input (f=0x%x) on %q: router=%v scalar=%v schema=%v giant=%v stream=%v",
				f, s, errRouter, errScalar, errSchema, errGiant, errStream)
		}
		for name, g := range map[string]diffRawEnvelope{
			"router": gotRouter, "scalar": gotScalar, "schema": gotSchema, "giant": gotGiant, "stream": gotStream,
		} {
			if !equalRawEnvelope(g, gotRuns) {
				t.Fatalf("raw NoValidateJSON %s vs runs mismatch on %q:\n  got=%#v\n  runs=%#v", name, s, g, gotRuns)
			}
		}
	}
	if checkStd && utf8.ValidString(s) {
		if hasStdPre127StrconvFloatBug(s) {
			return
		}
		gotStd, errStd := decodeViaStdGenericWithOptsAndInit[diffRawEnvelope](s, f, initFn)
		if (errRuns != nil) != (errStd != nil) {
			t.Fatalf("raw envelope vs encoding/json err mismatch (f=0x%x) on %q:\n  runs=%v\n  std=%v", f, s, errRuns, errStd)
		}
		if errStd == nil && !equalRawEnvelope(gotRuns, gotStd) {
			t.Fatalf("raw envelope vs encoding/json value mismatch (f=0x%x) on %q:\n  runs=%#v\n  std=%#v", f, s, gotRuns, gotStd)
		}
	}
}

func checkRawEnvelopeFiveWay(t *testing.T, s string, f uint64) {
	t.Helper()
	checkStd := f == diffStdOpts || f == diffStdDisUnknown || f == diffUseNumberOpts
	for _, initFn := range rawEnvelopeInitializerModes() {
		checkRawEnvelopeFiveWayWithInit(t, s, f, checkStd, initFn)
	}
}

func diffSeedCorpus() []string {
	pad := strings.Repeat("x", 320)
	deep65 := strings.Repeat("[", 65) + "1e999" + strings.Repeat("]", 65)
	return []string{
		`{}`,
		`null`,
		`{"id":42,"u64":18446744073709551615,"f32":3.402823469e+38,"f64":1.7976931348623157e+308,"flag":true,"name":"sonic","ptr_int":-9,"ptr_int2":12,"nums":[1,2,3],"nums2":[4,5],"tags":["a","b"],"fixed":[7,8],"kids":[{"code":1,"tag":"k1"}],"child":{"code":5,"tag":"t"}}`,
		`{"ptr_int2":20,"ptr_int":10,"nums2":[9,8],"nums":[1,2],"id":5}`,
		`{"id":5,"ptr_int":10,"ptr_int2":20,"nums":[1,2],"nums2":[9,8]}`,
		`{"ptr_int2":null,"ptr_int":77,"nums2":null,"nums":[3]}`,
		`{"id":42,"f32":1.0000000596046448,"name":"sonic","meta":{"k":"v"},"child":{"code":5,"tag":"t"},"str_int":"123"}`,
		`{"f32":1.0000000596046447}`,
		`{"f32":-1.0000000596046448}`,
		`{"f32":1.401298464324817e-45}`,
		`{"id":1,"id":2,"nums":[1,2],"nums":[3],"fixed":[9,10],"fixed":[11],"kids":[{"code":1,"tag":"a"}],"kids":[{"tag":"b"}],"meta":{"a":"1"},"meta":{"b":"2"},"child":{"code":1,"tag":"x"},"child":{"tag":"y"}}`,
		`{"nums":[1,2],"nums":null,"nums":[4,5],"fixed":[5,6],"fixed":null,"fixed":[7],"kids":[{"code":3}],"kids":null,"kids":[{"tag":"z"}],"ptr_int":10,"ptr_int":null}`,
		`{"ID":10,"id":20,"NaMe":"upper","name":"lower","n\u0061me":"escaped"}`,
		`{"id":7,"skip_obj":{"a":[1,true,false,null,"` + pad + `"]},"name":"after-skip","tags":["\uD83D\uDE00","\uD800"]}`,
		`{"f32":3.4028235e+38}`,
		`{"f32":3.4028236e+38}`,
		`{"str_int":"01"}`,
		`{"str_int":"+0"}`,
		`{"str_int":null}`,
		`{"id":1, "raw":{"big":1.5e+9999,"arr":[1,2,3]}, "note":"ok", "val":1.5}`,
		`{"id":2, "raw":` + deep65 + `, "note":"deep", "val":2.5}`,
		`{"id":3, "raw":[1], "note":"badval", "val":1e999}`,
		`{"id":4, "raw":[1e999], "note":"dyn", "val":1e999, "items":[{"value":1e999}], "anys":[1e999]}`,
		`{"id":5, "items":[], "items":[{"value":1e999}], "anys":[], "anys":[-1e999]}`,
		`{"val":null, "val":1e999}`,
		`{"id":6, "unknown_big":1e999, "unknown_nested":{"x":-1e999,"arr":[1e999]}}`,
		`{"kids":[], "kids":[{"code":1}]}`,
		`{"f64":1e-999}`,
		`{"f64":-1e-999}`,
		`1e999x`,
		`1e999.2`,
		`1e+`,
		`01`,
		`{"id":1, "raw":invalid, "note":"bad"}`,
		`{"name":"bad\qescape"}`,
		`{"name":"ctrl` + "\x01" + `"}`,
		`{"name":"invalid-utf8-` + "\xff\xfe" + `"}`,
		`{"":-}`,
		`[-]`,
		`10000000000000000000+`,
		`1e999+`,
		"\"\x00\"\x80",
		`{"id":1} trailing`,
		`[1, 2, {"a": "b", "c": [true, false, null, -0, 1e-10]}]`,
		`f`,
		"{\"\":\"\x800\"",
		`"\\0`,
		`"\0`,
		`"\u"`,
		`"\u1G`,
		`"\uD800\u0041"`,
		`-++`,
		`{"rAw":{"0":1+00+00000`,
		`"\000000`,
		"\"\x01\\000000",
		`"\u1G000000`,
		"\"\\u\x80\x11",
		"\"\\u\x80\"",
		"\"\\u1\x80\"",
		"\"\\uD800\x11",
		"\"\\uD800a",
		"\"\\uD800\\u\x11",
		"{\"id\":1,\"id\":2,\"nums\":[1,2],\"nums\":[3],\"fixed\":[9,10],\"fixed\":[1\x00",
		"{\"id\":1,\"id\":2,\"nums\":[1,2],\"nums\":[3],\"fixed\":[9,10],\"fixed\":[0\x00",
		"{\"id\":1,\"id\":2,\"nums\":[1,2],\"nums\":[3],\"fixed\":[9,10],\"nums\":[12\x00",
		"{\"id\":1,\"id\":2,\"nums\":[1,2],\"nums\":[3],\"fixed\":[9,1],\"nums\":[0.5\x00",
		"{\"id\":1,\"id\":2,\"nums\":[1,2],\"nums\":[3],\"fixed\":[9,1],\"nums\":[1.5\x00",
		"{\"id\":1,\"id\":2,\"nums\":[1,2],\"nums\":[3],\"fixed\":[9,1],\"nums\":[1e2\x00",
		"{\"id\":1,\"id\":2,\"nums\":[1,2],\"nums\":[3],\"fixed\":[9,1],\"nums\":[true\x00",
		"{\"id\":1,\"id\":2,\"nums\":[1,2],\"nums\":[3],\"fixed\":[9],\"nums\":[false\x00",
		"{\"id\":1,\"id\":2,\"nums\":[1,2],\"nums\":[3],\"fixed\":[9,1],\"nums\":[null\x00",
		"[" + strings.Repeat(" ", 44) + "1.0000000596046448\x00",
	}
}

func TestSixWayDifferentialCorpus(t *testing.T) {
	resetDiffPathStats()
	cleanFlat := `{"id":42,"f32":1.0000000596046448,"name":"hit","ptr_int":7,"nums":[1,2]}`
	var hitTarget diffFlatStruct
	if _, end, err := DecodeOne(cleanFlat, diffStdOpts, &hitTarget); checkSingleDocEnd(cleanFlat, end, err) != nil {
		t.Fatalf("DecodeOne cleanFlat failed: %v", err)
	}
	var reuseHit diffFlatStruct
	flatInitializerModes()[0](&reuseHit)
	if _, end, err := DecodeOne(cleanFlat, diffStdOpts, &reuseHit); checkSingleDocEnd(cleanFlat, end, err) != nil {
		t.Fatalf("DecodeOne cleanFlat reuse failed: %v", err)
	}
	cleanSparse := `{"id":42,"name":"hit","skip_blob":"` + strings.Repeat("b", 400) + `"}`
	if _, okSchema, err := decodeViaSchemaOrFullWithInit[diffFlatStruct](cleanSparse, diffStdOpts, nil); err != nil || !okSchema {
		t.Fatalf("expected forced Schema fast path to hit on cleanSparse: ok=%v err=%v", okSchema, err)
	}

	corpus := diffSeedCorpus()
	// Layer 1: Standard-compatible option presets compared against matching encoding/json.
	// Layer 2: Sonic extension presets checked for 6-way internal equivalence + dedicated contract assertions below.
	optSets := []struct {
		f        uint64
		checkStd bool
	}{
		{diffStdOpts, true},
		{diffStdDisUnknown, true},
		{diffUseNumberOpts, true},
		{diffStrictURCOpts, false},
		{diffCaseSensOpts, false},
		{diffFastCopyOpts, false},
	}
	inits := flatInitializerModes()

	for idx, s := range corpus {
		for _, os := range optSets {
			checkGenericSixWay[diffFlatStruct](t, s, os.f, os.checkStd, true)
			// diffRichStruct includes ,string which matches Go 1.27 jsonv2 on diffStdUsesJSONV2.
			checkGenericSixWay[diffRichStruct](t, s, os.f, os.checkStd && diffStdUsesJSONV2 && !diffStdJSONV2Pre1273, false)
		}
		for _, initFn := range inits {
			checkGenericSixWayWithInit[diffFlatStruct](t, s, diffStdOpts, true, true, initFn)
		}
		checkAliasedSideEffects(t, s)

		// Sequential two-decode dirty reuse into the same struct across paths.
		prev := corpus[(idx+len(corpus)-1)%len(corpus)]
		if json.Valid([]byte(prev)) {
			checkGenericSixWayWithInit[diffFlatStruct](t, s, diffStdOpts, true, true, func(d *diffFlatStruct) {
				_ = json.Unmarshal([]byte(prev), d)
			})
		}

		if json.Valid([]byte(s)) {
			checkGenericSixWay[diffFlatStruct](t, s, diffNoValValidOpts, false, true)
			checkGenericSixWay[diffRichStruct](t, s, diffNoValValidOpts, false, false)
		}
		for _, af := range []uint64{diffStdOpts, diffFastCopyOpts, diffUseNumberOpts, diffUseInt64Opts} {
			checkAnyFiveWay(t, s, af)
		}
		checkRawEnvelopeFiveWay(t, s, diffStdOpts)
		checkRawEnvelopeFiveWay(t, s, diffStdDisUnknown)
		checkRawEnvelopeFiveWay(t, s, diffUseNumberOpts)
		if json.Valid([]byte(s)) {
			checkRawEnvelopeFiveWay(t, s, diffNoValValidOpts)
		}
	}

	if diffStatsExecs.Load() == 0 || diffStatsSchemaHit.Load() == 0 {
		t.Fatalf("expected non-empty differential corpus & schema hits, got execs=%d schemaHit=%d schemaFallback=%d",
			diffStatsExecs.Load(), diffStatsSchemaHit.Load(), diffStatsSchemaFallback.Load())
	}
	t.Logf("[corpus-stats] execs=%d schemaHit=%d schemaFallback=%d",
		diffStatsExecs.Load(), diffStatsSchemaHit.Load(), diffStatsSchemaFallback.Load())
}

func TestExtensionPresetsExplicitContracts(t *testing.T) {
	// 1. OptionCaseSensitive: rejects case-folded field names while matching exact field names across all 6 paths.
	csInput := `{"ID":10,"id":20,"NaMe":"folded"}`
	checkGenericSixWay[diffFlatStruct](t, csInput, diffCaseSensOpts, false, true)
	gotCS, err := decodeViaRunsWithInit[diffFlatStruct](csInput, diffCaseSensOpts, nil)
	if err != nil || gotCS.ID != 20 || gotCS.Name != "" {
		t.Fatalf("OptionCaseSensitive contract mismatch: err=%v got=%+v", err, gotCS)
	}

	// 2. UseInt64: decodes integers into int64, floats into float64, and rejects 1e999 into bare any.
	i64Input := `[42, -9007199254740993, 1.5]`
	checkAnyFiveWay(t, i64Input, diffUseInt64Opts)
	gotI64, err := decodeViaRunsWithInit[any](i64Input, diffUseInt64Opts, nil)
	if err != nil {
		t.Fatalf("UseInt64 decode failed: %v", err)
	}
	arr := gotI64.([]any)
	if arr[0] != int64(42) || arr[1] != int64(-9007199254740993) || arr[2] != float64(1.5) {
		t.Fatalf("UseInt64 element types mismatch: %#v", arr)
	}
	checkAnyFiveWay(t, `[1e999]`, diffUseInt64Opts)
	if _, err := decodeViaRunsWithInit[any](`[1e999]`, diffUseInt64Opts, nil); err == nil {
		t.Fatal("expected UseInt64 to reject [1e999] into bare any")
	}

	// 3. OptionUseUnicodeErrors (_F_disable_urc): invalid UTF-16 surrogate escape sequences
	// replace with U+FFFD under diffStdOpts, and error across all 6 paths under diffStrictURCOpts.
	for _, badEsc := range []string{`{"name":"\uD800"}`, `{"name":"\uDFFF"}`, `{"name":"\uD800\u0041"}`} {
		checkGenericSixWay[diffFlatStruct](t, badEsc, diffStdOpts, true, true)
		checkGenericSixWay[diffFlatStruct](t, badEsc, diffStrictURCOpts, false, true)
		if _, err := decodeViaRunsWithInit[diffFlatStruct](badEsc, diffStrictURCOpts, nil); err == nil {
			t.Fatalf("expected _F_disable_urc error on %q", badEsc)
		}
	}
}

func FuzzSixWayDifferential(f *testing.F) {
	if err := ident.VerifyFromEnv(); err != nil {
		f.Fatalf("fuzz coordinator backend identity mismatch: %v", err)
	}
	var workerIdentOnce sync.Once
	resetDiffPathStats()
	if os.Getenv("SONIC_FUZZ_STATS") == "1" {
		f.Cleanup(func() {
			fmt.Fprintf(os.Stderr, "[fuzz-stats pid=%d] execs=%d schemaHit=%d schemaFallback=%d\n",
				os.Getpid(),
				diffStatsExecs.Load(),
				diffStatsSchemaHit.Load(),
				diffStatsSchemaFallback.Load())
		})
	}
	for idx, s := range diffSeedCorpus() {
		f.Add([]byte(s), uint8(idx))
	}
	inits := flatInitializerModes()
	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		workerIdentOnce.Do(func() {
			if err := ident.VerifyFromEnv(); err != nil {
				t.Fatalf("fuzz worker backend identity mismatch: %v", err)
			}
		})
		if len(data) > 64<<10 {
			return
		}
		s := string(data)
		switch mode % 7 {
		case 0:
			checkGenericSixWay[diffFlatStruct](t, s, diffStdOpts, true, true)
			checkGenericSixWay[diffRichStruct](t, s, diffStdOpts, diffStdUsesJSONV2 && !diffStdJSONV2Pre1273, false)
		case 1:
			checkGenericSixWay[diffFlatStruct](t, s, diffStdDisUnknown, true, true)
			checkGenericSixWay[diffRichStruct](t, s, diffStdDisUnknown, diffStdUsesJSONV2 && !diffStdJSONV2Pre1273, false)
		case 2:
			checkGenericSixWay[diffFlatStruct](t, s, diffCaseSensOpts, false, true)
			checkGenericSixWay[diffRichStruct](t, s, diffCaseSensOpts, false, false)
			checkGenericSixWay[diffFlatStruct](t, s, diffStrictURCOpts, false, true)
			checkGenericSixWay[diffRichStruct](t, s, diffStrictURCOpts, false, false)
		case 3:
			checkGenericSixWay[diffFlatStruct](t, s, diffFastCopyOpts, false, true)
			checkGenericSixWay[diffRichStruct](t, s, diffFastCopyOpts, false, false)
			if json.Valid(data) {
				checkGenericSixWay[diffFlatStruct](t, s, diffNoValValidOpts, false, true)
				checkGenericSixWay[diffRichStruct](t, s, diffNoValValidOpts, false, false)
			}
		case 4:
			checkAnyFiveWay(t, s, diffStdOpts)
			checkAnyFiveWay(t, s, diffUseNumberOpts)
			checkAnyFiveWay(t, s, diffUseInt64Opts)
		case 5:
			checkRawEnvelopeFiveWay(t, s, diffStdOpts)
			checkRawEnvelopeFiveWay(t, s, diffUseNumberOpts)
			if json.Valid(data) {
				checkRawEnvelopeFiveWay(t, s, diffNoValValidOpts)
			}
		case 6:
			initFn := inits[int(mode>>3)%len(inits)]
			checkGenericSixWayWithInit[diffFlatStruct](t, s, diffStdOpts, true, true, initFn)
			checkAliasedSideEffects(t, s)
		}
	})
}
