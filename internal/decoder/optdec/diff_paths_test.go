//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
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

type diffRawEnvelope struct {
	ID   int             `json:"id"`
	Raw  json.RawMessage `json:"raw"`
	Note string          `json:"note"`
	Val  any             `json:"val"`
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

func decodeViaRunsWithInit[T any](s string, f uint64, initFn func(*T)) (T, error) {
	var dst T
	if initFn != nil {
		initFn(&dst)
	}
	etp, vp, dec, _, _, err := unpackAndCompileRoot(&dst)
	if err != nil {
		return dst, err
	}
	p := newParserImpl(s, 0, f)
	defer p.free()
	ctx, err := newContextOnParserWithPlan(p, f, etp, nil)
	if p.Utf8Inv {
		s = p.Json
	}
	if err == nil {
		err = decodeRootFromDom(vp, dec, ctx)
	}
	endPos := p.Pos()
	if endPos > len(s) {
		endPos = len(s)
	}
	return dst, checkSingleDocEnd(s, endPos, fix_error(s, 0, err))
}

func decodeViaSchemaOrFullWithInit[T any](s string, f uint64, initFn func(*T)) (T, bool, error) {
	var dst T
	if initFn != nil {
		initFn(&dst)
	}
	_, vp, dec, proj, _, err := unpackAndCompileRoot(&dst)
	if err != nil {
		return dst, false, err
	}
	if proj == nil || proj.kind == projFull || proj.kind == projScalar || proj.kind == projBytes {
		v, rerr := decodeViaRunsWithInit[T](s, f, initFn)
		return v, false, rerr
	}
	p := newParserLazy(s, 0, f)
	defer p.free()
	ctx := &p.ctx
	ctx.Parser = p
	p.strictURC = (f & (1 << _F_disable_urc)) != 0
	p.keepUseNumber = (f & (1 << _F_use_number)) != 0
	p.strictRawNum = true
	p.deferFloatOverflow = !p.keepUseNumber
	p.hasDeferredFloatInf = false
	ok, mutated := parseSchemaRoot(p, proj)
	if !ok {
		code := resetAndRunFullParse(p, mutated)
		if p.Utf8Inv {
			s = p.Json
		}
		if code != 0 {
			return dst, false, fix_error(s, 0, p.fixError(code))
		}
	} else if p.Utf8Inv {
		s = p.Json
	}
	err = decodeRootFromDom(vp, dec, ctx)
	endPos := p.Pos()
	if endPos > len(s) {
		endPos = len(s)
	}
	return dst, ok, checkSingleDocEnd(s, endPos, fix_error(s, 0, err))
}

func decodeViaGiantWithInit[T any](s string, f uint64, initFn func(*T)) (T, error) {
	var dst T
	if initFn != nil {
		initFn(&dst)
	}
	etp, vp, dec, _, _, err := unpackAndCompileRoot(&dst)
	if err != nil {
		return dst, err
	}
	sOut, endPos, err := decodeGiantOneCompiled(s, f, etp, vp, dec)
	return dst, checkSingleDocEnd(sOut, endPos, err)
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

func decodeViaChunkedStreamWithInit[T any](s string, f uint64, chunkSize int, initFn func(*T)) (T, error) {
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
	if err != nil {
		return dst, err
	}
	consumed := st.scanned + st.scanp
	if consumed > len(s) {
		consumed = len(s)
	}
	return dst, checkSingleDocEnd(s, consumed, nil)
}

func decodeViaStdGenericWithInit[T any](s string, disUnknown bool, initFn func(*T)) (T, error) {
	var dst T
	if initFn != nil {
		initFn(&dst)
	}
	if !disUnknown {
		err := json.Unmarshal([]byte(s), &dst)
		return dst, err
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&dst); err != nil {
		return dst, err
	}
	var extra any
	if dec.More() || dec.Decode(&extra) == nil || !json.Valid([]byte(s)) {
		return dst, &json.SyntaxError{Offset: int64(len(s))}
	}
	return dst, nil
}

func checkGenericSixWayWithInit[T any](t *testing.T, s string, f uint64, checkStd bool, withFlat bool, initFn func(*T)) {
	t.Helper()

	var gotRouter T
	if initFn != nil {
		initFn(&gotRouter)
	}
	sRouter, endRouter, errRouter := DecodeOne(s, f, &gotRouter)
	errRouter = checkSingleDocEnd(sRouter, endRouter, errRouter)

	gotRuns, errRuns := decodeViaRunsWithInit[T](s, f, initFn)
	gotSchema, _, errSchema := decodeViaSchemaOrFullWithInit[T](s, f, initFn)
	gotGiant, errGiant := decodeViaGiantWithInit[T](s, f, initFn)
	gotStream, errStream := decodeViaChunkedStreamWithInit[T](s, f, 7, initFn)

	var gotFlat T
	var errFlat error
	okFlat := false
	if withFlat {
		if initFn != nil {
			initFn(&gotFlat)
		}
		_, vp, _, _, flatSD, err := unpackAndCompileRoot(&gotFlat)
		if err != nil || flatSD == nil {
			t.Fatalf("expected flatSD != nil for %T, err=%v", gotFlat, err)
		}
		var endFlat int
		endFlat, okFlat = tryDecodeFlatStructForced(s, 0, f, unsafe.Pointer(vp), flatSD, false, true)
		if okFlat {
			errFlat = checkSingleDocEnd(s, endFlat, nil)
		}
	}

	noValSkip := (f & (1 << _F_no_validate_json)) != 0
	if !noValSkip {
		if (errRouter != nil) != (errRuns != nil) {
			t.Fatalf("%T router vs runs err mismatch (f=0x%x) on %q:\n  router=%v\n  runs=%v", gotRuns, f, s, errRouter, errRuns)
		}
		if (errSchema != nil) != (errRuns != nil) {
			t.Fatalf("%T schema vs runs err mismatch (f=0x%x) on %q:\n  schema=%v\n  runs=%v", gotRuns, f, s, errSchema, errRuns)
		}
		if (errGiant != nil) != (errRuns != nil) {
			t.Fatalf("%T giant vs runs err mismatch (f=0x%x) on %q:\n  giant=%v\n  runs=%v", gotRuns, f, s, errGiant, errRuns)
		}
		if (errStream != nil) != (errRuns != nil) {
			t.Fatalf("%T stream vs runs err mismatch (f=0x%x) on %q:\n  stream=%v\n  runs=%v", gotRuns, f, s, errStream, errRuns)
		}
		if okFlat && (errFlat != nil) != (errRuns != nil) {
			t.Fatalf("%T flat vs runs err mismatch (f=0x%x) on %q:\n  flat=%v\n  runs=%v", gotRuns, f, s, errFlat, errRuns)
		}
	} else if errRuns == nil {
		if errRouter != nil || errSchema != nil || errGiant != nil || errStream != nil || (okFlat && errFlat != nil) {
			t.Fatalf("%T NoValidateJSON failed on input accepted by runs (f=0x%x) on %q: router=%v schema=%v giant=%v stream=%v flat=%v",
				gotRuns, f, s, errRouter, errSchema, errGiant, errStream, errFlat)
		}
	}

	if errRuns == nil {
		if !reflect.DeepEqual(gotRouter, gotRuns) {
			t.Fatalf("%T router vs runs value mismatch (f=0x%x) on %q:\n  router=%#v\n  runs=%#v", gotRuns, f, s, gotRouter, gotRuns)
		}
		if !reflect.DeepEqual(gotSchema, gotRuns) {
			t.Fatalf("%T schema vs runs value mismatch (f=0x%x) on %q:\n  schema=%#v\n  runs=%#v", gotRuns, f, s, gotSchema, gotRuns)
		}
		if !reflect.DeepEqual(gotGiant, gotRuns) {
			t.Fatalf("%T giant vs runs value mismatch (f=0x%x) on %q:\n  giant=%#v\n  runs=%#v", gotRuns, f, s, gotGiant, gotRuns)
		}
		if !reflect.DeepEqual(gotStream, gotRuns) {
			t.Fatalf("%T stream vs runs value mismatch (f=0x%x) on %q:\n  stream=%#v\n  runs=%#v", gotRuns, f, s, gotStream, gotRuns)
		}
		if okFlat && !reflect.DeepEqual(gotFlat, gotRuns) {
			t.Fatalf("%T flat vs runs value mismatch (f=0x%x) on %q:\n  flat=%#v\n  runs=%#v", gotRuns, f, s, gotFlat, gotRuns)
		}
	}

	if checkStd && utf8.ValidString(s) {
		disUnknown := (f & (1 << _F_disable_unknown)) != 0
		gotStd, errStd := decodeViaStdGenericWithInit[T](s, disUnknown, initFn)
		// optdec intentionally rejects IEEE-754 float64 overflow in skipped unknown fields unless UseNumber is set.
		if errRuns != nil && errStd == nil && strings.Contains(errRuns.Error(), "float infinity") {
			return
		}
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
	if !utf8.ValidString(s) {
		return
	}
	// Shared external int64 observed directly after decode across Router, Runs, Schema, Giant, Stream, and encoding/json.
	sRouterV, sRunsV, sSchemaV, sGiantV, sStreamV, sStdV := int64(77), int64(77), int64(77), int64(77), int64(77), int64(77)
	dRouter := diffFlatStruct{PtrInt: &sRouterV, PtrInt2: &sRouterV}
	_, endR, errRouter := DecodeOne(s, diffStdOpts, &dRouter)
	errRouter = checkSingleDocEnd(s, endR, errRouter)
	_, errRuns := decodeViaRunsWithInit[diffFlatStruct](s, diffStdOpts, func(d *diffFlatStruct) {
		d.PtrInt, d.PtrInt2 = &sRunsV, &sRunsV
	})
	_, _, errSchema := decodeViaSchemaOrFullWithInit[diffFlatStruct](s, diffStdOpts, func(d *diffFlatStruct) {
		d.PtrInt, d.PtrInt2 = &sSchemaV, &sSchemaV
	})
	_, errGiant := decodeViaGiantWithInit[diffFlatStruct](s, diffStdOpts, func(d *diffFlatStruct) {
		d.PtrInt, d.PtrInt2 = &sGiantV, &sGiantV
	})
	_, errStream := decodeViaChunkedStreamWithInit[diffFlatStruct](s, diffStdOpts, 7, func(d *diffFlatStruct) {
		d.PtrInt, d.PtrInt2 = &sStreamV, &sStreamV
	})
	dStd := diffFlatStruct{PtrInt: &sStdV, PtrInt2: &sStdV}
	errStd := json.Unmarshal([]byte(s), &dStd)
	if errRuns == nil && errStd == nil {
		if sRouterV != sStdV || sRunsV != sStdV || sSchemaV != sStdV || sGiantV != sStdV || sStreamV != sStdV {
			t.Fatalf("shared pointer target value diverged on %q: router=%d runs=%d schema=%d giant=%d stream=%d std=%d",
				s, sRouterV, sRunsV, sSchemaV, sGiantV, sStreamV, sStdV)
		}
	}
	_ = errRouter
	_ = errSchema
	_ = errGiant
	_ = errStream
}

func checkAnyFiveWay(t *testing.T, s string, f uint64) {
	t.Helper()
	var gotRouter any
	sRouter, endRouter, errRouter := DecodeOne(s, f, &gotRouter)
	errRouter = checkSingleDocEnd(sRouter, endRouter, errRouter)
	gotRuns, errRuns := decodeViaRunsWithInit[any](s, f, nil)
	gotGiant, errGiant := decodeViaGiantWithInit[any](s, f, nil)
	gotStream, errStream := decodeViaChunkedStreamWithInit[any](s, f, 7, nil)

	if (errRouter != nil) != (errRuns != nil) || (errGiant != nil) != (errRuns != nil) || (errStream != nil) != (errRuns != nil) {
		t.Fatalf("any err mismatch (f=0x%x) on %q: router=%v runs=%v giant=%v stream=%v", f, s, errRouter, errRuns, errGiant, errStream)
	}
	if errRuns == nil {
		if !reflect.DeepEqual(gotRouter, gotRuns) || !reflect.DeepEqual(gotGiant, gotRuns) || !reflect.DeepEqual(gotStream, gotRuns) {
			t.Fatalf("any value mismatch (f=0x%x) on %q:\n  router=%#v\n  runs=%#v\n  giant=%#v\n  stream=%#v",
				f, s, gotRouter, gotRuns, gotGiant, gotStream)
		}
	}
	if (f == diffStdOpts || f == diffUseNumberOpts) && utf8.ValidString(s) {
		var gotStd any
		var errStd error
		if f == diffUseNumberOpts {
			dec := json.NewDecoder(strings.NewReader(s))
			dec.UseNumber()
			errStd = dec.Decode(&gotStd)
			if errStd == nil && !json.Valid([]byte(s)) {
				errStd = &json.SyntaxError{Offset: int64(len(s))}
			}
		} else {
			errStd = json.Unmarshal([]byte(s), &gotStd)
		}
		if (errRuns != nil) != (errStd != nil) {
			t.Fatalf("any vs std err mismatch (f=0x%x) on %q: runs=%v std=%v", f, s, errRuns, errStd)
		}
		if errStd == nil && !reflect.DeepEqual(gotRuns, gotStd) {
			t.Fatalf("any vs std value mismatch (f=0x%x) on %q:\n  runs=%#v\n  std=%#v", f, s, gotRuns, gotStd)
		}
	}
}

func checkRawEnvelopeFiveWay(t *testing.T, s string, f uint64) {
	t.Helper()
	var gotRouter diffRawEnvelope
	sRouter, endRouter, errRouter := DecodeOne(s, f, &gotRouter)
	errRouter = checkSingleDocEnd(sRouter, endRouter, errRouter)
	gotRuns, errRuns := decodeViaRunsWithInit[diffRawEnvelope](s, f, nil)
	gotSchema, _, errSchema := decodeViaSchemaOrFullWithInit[diffRawEnvelope](s, f, nil)
	gotGiant, errGiant := decodeViaGiantWithInit[diffRawEnvelope](s, f, nil)
	gotStream, errStream := decodeViaChunkedStreamWithInit[diffRawEnvelope](s, f, 7, nil)

	noValSkip := (f & (1 << _F_no_validate_json)) != 0
	if !noValSkip {
		if (errRouter != nil) != (errRuns != nil) ||
			(errSchema != nil) != (errRuns != nil) ||
			(errGiant != nil) != (errRuns != nil) ||
			(errStream != nil) != (errRuns != nil) {
			t.Fatalf("raw envelope err mismatch (f=0x%x) on %q: router=%v runs=%v schema=%v giant=%v stream=%v",
				f, s, errRouter, errRuns, errSchema, errGiant, errStream)
		}
	} else if errRuns == nil {
		if errRouter != nil || errSchema != nil || errGiant != nil || errStream != nil {
			t.Fatalf("raw envelope NoValidateJSON failed on valid input (f=0x%x) on %q: router=%v schema=%v giant=%v stream=%v",
				f, s, errRouter, errSchema, errGiant, errStream)
		}
	}
	if errRuns == nil {
		for name, g := range map[string]diffRawEnvelope{
			"router": gotRouter,
			"schema": gotSchema,
			"giant":  gotGiant,
			"stream": gotStream,
		} {
			if g.ID != gotRuns.ID || g.Note != gotRuns.Note || !bytes.Equal(g.Raw, gotRuns.Raw) || !reflect.DeepEqual(g.Val, gotRuns.Val) {
				t.Fatalf("raw %s vs runs mismatch on %q: %+v vs %+v", name, s, g, gotRuns)
			}
		}
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
		`{"id":1, "raw":invalid, "note":"bad"}`,
		`{"name":"bad\qescape"}`,
		`{"name":"ctrl` + "\x01" + `"}`,
		`{"name":"invalid-utf8-` + "\xff\xfe" + `"}`,
		`{"":-}`,
		`[-]`,
		`{"id":1} trailing`,
		`[1, 2, {"a": "b", "c": [true, false, null, -0, 1e-10]}]`,
	}
}

func TestSixWayDifferentialCorpus(t *testing.T) {
	// Verify forced fast-path hit assertions on canonical eligible input before sweeping corpus.
	cleanFlat := `{"id":42,"f32":1.0000000596046448,"name":"hit","ptr_int":7,"nums":[1,2]}`
	var hitTarget diffFlatStruct
	_, vpHit, _, _, flatSDHit, err := unpackAndCompileRoot(&hitTarget)
	if err != nil || flatSDHit == nil {
		t.Fatalf("unpackAndCompileRoot failed: %v", err)
	}
	if _, ok := tryDecodeFlatStructForced(cleanFlat, 0, diffStdOpts, vpHit, flatSDHit, false, true); !ok {
		t.Fatal("expected forced Flat fast path to hit on cleanFlat")
	}
	cleanSparse := `{"id":42,"name":"hit","skip_blob":"` + strings.Repeat("b", 400) + `"}`
	if _, okSchema, err := decodeViaSchemaOrFullWithInit[diffFlatStruct](cleanSparse, diffStdOpts, nil); err != nil || !okSchema {
		t.Fatalf("expected forced Schema fast path to hit on cleanSparse: ok=%v err=%v", okSchema, err)
	}

	corpus := diffSeedCorpus()
	optSets := []struct {
		f        uint64
		checkStd bool
	}{
		{diffStdOpts, true},
		{diffStdDisUnknown, true},
		{diffStrictURCOpts, false},
		{diffCaseSensOpts, false},
		{diffFastCopyOpts, false},
	}
	inits := flatInitializerModes()

	for idx, s := range corpus {
		for _, os := range optSets {
			checkGenericSixWay[diffFlatStruct](t, s, os.f, os.checkStd, true)
			// diffRichStruct includes ,string which delegates to strconv (Go 1.27 jsonv2 STRING-GONUM-PLUS semantics).
			checkGenericSixWay[diffRichStruct](t, s, os.f, false, false)
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
		checkRawEnvelopeFiveWay(t, s, diffNoValValidOpts)
	}
}

func FuzzSixWayDifferential(f *testing.F) {
	for idx, s := range diffSeedCorpus() {
		f.Add([]byte(s), uint8(idx))
	}
	inits := flatInitializerModes()
	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		if len(data) > 64<<10 {
			return
		}
		s := string(data)
		switch mode % 7 {
		case 0:
			checkGenericSixWay[diffFlatStruct](t, s, diffStdOpts, true, true)
			checkGenericSixWay[diffRichStruct](t, s, diffStdOpts, false, false)
		case 1:
			checkGenericSixWay[diffFlatStruct](t, s, diffStdDisUnknown, true, true)
			checkGenericSixWay[diffRichStruct](t, s, diffStdDisUnknown, false, false)
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
			checkRawEnvelopeFiveWay(t, s, diffNoValValidOpts)
		case 6:
			initFn := inits[int(mode>>3)%len(inits)]
			checkGenericSixWayWithInit[diffFlatStruct](t, s, diffStdOpts, true, true, initFn)
			checkAliasedSideEffects(t, s)
		}
	})
}
