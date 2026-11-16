//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

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

// diffFlatStruct is eligible for all 5 paths: flat, schema_proj, runs, giant, and encoding/json.
type diffFlatStruct struct {
	ID     int64       `json:"id"`
	U64    uint64      `json:"u64"`
	F32    float32     `json:"f32"`
	F64    float64     `json:"f64"`
	Flag   bool        `json:"flag"`
	Name   string      `json:"name"`
	PtrInt *int64      `json:"ptr_int"`
	Nums   []int       `json:"nums"`
	Tags   []string    `json:"tags"`
	Fixed  [2]int      `json:"fixed"`
	Kids   []diffChild `json:"kids"`
	Child  diffChild   `json:"child"`
}

// diffRichStruct includes map and ,string fields for schema_proj, runs, giant, and encoding/json.
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

func decodeViaRuns[T any](s string, f uint64) (T, error) {
	var dst T
	etp, vp, dec, proj, _, err := unpackAndCompileRoot(&dst)
	if err != nil {
		return dst, err
	}
	pf := f
	if proj != nil && proj.kind == projRaw {
		pf |= 1 << _F_use_number
	}
	p := newParserImpl(s, 0, pf)
	defer p.free()
	ctx, err := newContextOnParserWithPlan(p, pf, etp, nil)
	if p.Utf8Inv {
		s = p.Json
	}
	if err == nil {
		err = dec.FromDom(vp, ctx.Root(), ctx)
	}
	endPos := p.Pos()
	if endPos > len(s) {
		endPos = len(s)
	}
	return dst, checkSingleDocEnd(s, endPos, fix_error(s, 0, err))
}

func decodeViaSchemaOrFull[T any](s string, f uint64) (T, bool, error) {
	var dst T
	_, vp, dec, proj, _, err := unpackAndCompileRoot(&dst)
	if err != nil {
		return dst, false, err
	}
	if proj == nil || proj.kind == projFull || proj.kind == projScalar || proj.kind == projBytes {
		v, rerr := decodeViaRuns[T](s, f)
		return v, false, rerr
	}
	p := newParserLazy(s, 0, f)
	defer p.free()
	ctx := &p.ctx
	ctx.Parser = p
	p.strictURC = (f & (1 << _F_disable_urc)) != 0
	p.keepUseNumber = (f & (1 << _F_use_number)) != 0
	p.strictRawNum = true
	ok, mutated := parseSchemaRoot(p, proj)
	if !ok {
		if proj.kind == projRaw {
			p.options |= 1 << _F_use_number
		}
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
	err = dec.FromDom(vp, ctx.Root(), ctx)
	endPos := p.Pos()
	if endPos > len(s) {
		endPos = len(s)
	}
	return dst, ok, checkSingleDocEnd(s, endPos, fix_error(s, 0, err))
}

func decodeViaGiant[T any](s string, f uint64) (T, error) {
	var dst T
	etp, vp, dec, proj, _, err := unpackAndCompileRoot(&dst)
	if err != nil {
		return dst, err
	}
	gf := f
	if proj != nil && proj.kind == projRaw {
		gf |= 1 << _F_use_number
	}
	sOut, endPos, err := decodeGiantOneCompiled(s, gf, etp, vp, dec)
	return dst, checkSingleDocEnd(sOut, endPos, err)
}

func decodeViaStdGeneric[T any](s string, disUnknown bool) (T, error) {
	var dst T
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

func checkGenericFiveWay[T any](t *testing.T, s string, f uint64, checkStd bool, withFlat bool) {
	t.Helper()

	var gotRouter T
	sRouter, endRouter, errRouter := DecodeOne(s, f, &gotRouter)
	errRouter = checkSingleDocEnd(sRouter, endRouter, errRouter)

	gotRuns, errRuns := decodeViaRuns[T](s, f)
	gotSchema, _, errSchema := decodeViaSchemaOrFull[T](s, f)
	gotGiant, errGiant := decodeViaGiant[T](s, f)

	var gotFlat T
	var errFlat error
	okFlat := false
	if withFlat {
		_, vp, _, _, flatSD, err := unpackAndCompileRoot(&gotFlat)
		if err != nil || flatSD == nil {
			t.Fatalf("expected flatSD != nil for %T, err=%v", gotFlat, err)
		}
		var endFlat int
		endFlat, okFlat = tryDecodeFlatStruct(s, 0, f, unsafe.Pointer(vp), flatSD, false, true)
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
		if okFlat && (errFlat != nil) != (errRuns != nil) {
			t.Fatalf("%T flat vs runs err mismatch (f=0x%x) on %q:\n  flat=%v\n  runs=%v", gotRuns, f, s, errFlat, errRuns)
		}
	} else if errRuns == nil {
		if errRouter != nil || errSchema != nil || errGiant != nil || (okFlat && errFlat != nil) {
			t.Fatalf("%T NoValidateJSON failed on input accepted by runs (f=0x%x) on %q: router=%v schema=%v giant=%v flat=%v", gotRuns, f, s, errRouter, errSchema, errGiant, errFlat)
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
		if okFlat && !reflect.DeepEqual(gotFlat, gotRuns) {
			t.Fatalf("%T flat vs runs value mismatch (f=0x%x) on %q:\n  flat=%#v\n  runs=%#v", gotRuns, f, s, gotFlat, gotRuns)
		}
	}

	if checkStd && utf8.ValidString(s) {
		disUnknown := (f & (1 << _F_disable_unknown)) != 0
		gotStd, errStd := decodeViaStdGeneric[T](s, disUnknown)
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

func checkAnyFourWay(t *testing.T, s string, f uint64) {
	t.Helper()
	var gotRouter any
	sRouter, endRouter, errRouter := DecodeOne(s, f, &gotRouter)
	errRouter = checkSingleDocEnd(sRouter, endRouter, errRouter)
	gotRuns, errRuns := decodeViaRuns[any](s, f)
	gotGiant, errGiant := decodeViaGiant[any](s, f)

	if (errRouter != nil) != (errRuns != nil) || (errGiant != nil) != (errRuns != nil) {
		t.Fatalf("any err mismatch (f=0x%x) on %q: router=%v runs=%v giant=%v", f, s, errRouter, errRuns, errGiant)
	}
	if errRuns == nil {
		if !reflect.DeepEqual(gotRouter, gotRuns) || !reflect.DeepEqual(gotGiant, gotRuns) {
			t.Fatalf("any value mismatch (f=0x%x) on %q:\n  router=%#v\n  runs=%#v\n  giant=%#v", f, s, gotRouter, gotRuns, gotGiant)
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

func checkRawEnvelopeFourWay(t *testing.T, s string, f uint64) {
	t.Helper()
	var gotRouter diffRawEnvelope
	sRouter, endRouter, errRouter := DecodeOne(s, f, &gotRouter)
	errRouter = checkSingleDocEnd(sRouter, endRouter, errRouter)
	gotSchema, _, errSchema := decodeViaSchemaOrFull[diffRawEnvelope](s, f)
	gotGiant, errGiant := decodeViaGiant[diffRawEnvelope](s, f)

	noValSkip := (f & (1 << _F_no_validate_json)) != 0
	if !noValSkip {
		if (errRouter != nil) != (errSchema != nil) || (errGiant != nil) != (errSchema != nil) {
			if !(errGiant != nil && strings.Contains(errGiant.Error(), "float infinity")) {
				t.Fatalf("raw envelope err mismatch (f=0x%x) on %q: router=%v schema=%v giant=%v", f, s, errRouter, errSchema, errGiant)
			}
			return
		}
	} else {
		if (errRouter != nil) != (errSchema != nil) {
			t.Fatalf("raw envelope router vs schema err mismatch (f=0x%x) on %q: router=%v schema=%v", f, s, errRouter, errSchema)
		}
		if errSchema != nil && errGiant == nil {
			t.Fatalf("raw envelope schema failed while giant succeeded (f=0x%x) on %q: schema=%v", f, s, errSchema)
		}
		if errGiant != nil {
			return
		}
	}
	if errSchema == nil {
		if gotRouter.ID != gotSchema.ID || gotRouter.Note != gotSchema.Note || !bytes.Equal(gotRouter.Raw, gotSchema.Raw) {
			t.Fatalf("raw router vs schema mismatch on %q: %+v vs %+v", s, gotRouter, gotSchema)
		}
		if gotGiant.ID != gotSchema.ID || gotGiant.Note != gotSchema.Note || !bytes.Equal(gotGiant.Raw, gotSchema.Raw) {
			t.Fatalf("raw giant vs schema mismatch on %q: %+v vs %+v", s, gotGiant, gotSchema)
		}
	}
}

func diffSeedCorpus() []string {
	pad := strings.Repeat("x", 320)
	return []string{
		`{}`,
		`null`,
		`{"id":42,"u64":18446744073709551615,"f32":3.402823469e+38,"f64":1.7976931348623157e+308,"flag":true,"name":"sonic","ptr_int":-9,"nums":[1,2,3],"tags":["a","b"],"fixed":[7,8],"kids":[{"code":1,"tag":"k1"}],"child":{"code":5,"tag":"t"}}`,
		`{"id":42,"f32":3.402823469e+38,"name":"sonic","meta":{"k":"v"},"child":{"code":5,"tag":"t"},"str_int":"123"}`,
		`{"id":1,"id":2,"nums":[1,2],"nums":[3],"fixed":[9,10],"fixed":[11],"kids":[{"code":1,"tag":"a"}],"kids":[{"tag":"b"}],"meta":{"a":"1"},"meta":{"b":"2"},"child":{"code":1,"tag":"x"},"child":{"tag":"y"}}`,
		`{"nums":[1,2],"nums":null,"nums":[4,5],"fixed":[5,6],"fixed":null,"fixed":[7],"kids":[{"code":3}],"kids":null,"kids":[{"tag":"z"}],"ptr_int":10,"ptr_int":null}`,
		`{"ID":10,"id":20,"NaMe":"upper","name":"lower","n\u0061me":"escaped"}`,
		`{"id":7,"skip_obj":{"a":[1,true,false,null,"` + pad + `"]},"name":"after-skip","tags":["\uD83D\uDE00","\uD800"]}`,
		`{"f32":3.4028235e+38}`,
		`{"f32":3.4028236e+38}`,
		`{"str_int":"01"}`,
		`{"str_int":"+0"}`,
		`{"str_int":null}`,
		`{"id":1, "raw":{"big":1.5e+9999,"arr":[1,2,3]}, "note":"ok"}`,
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

func TestFiveWayDifferentialCorpus(t *testing.T) {
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

	for _, s := range corpus {
		for _, os := range optSets {
			checkGenericFiveWay[diffFlatStruct](t, s, os.f, os.checkStd, true)
			// diffRichStruct includes ,string which delegates to strconv (Go 1.27 jsonv2 STRING-GONUM-PLUS semantics).
			checkGenericFiveWay[diffRichStruct](t, s, os.f, false, false)
		}
		if json.Valid([]byte(s)) {
			checkGenericFiveWay[diffFlatStruct](t, s, diffNoValValidOpts, false, true)
			checkGenericFiveWay[diffRichStruct](t, s, diffNoValValidOpts, false, false)
		}
		for _, af := range []uint64{diffStdOpts, diffFastCopyOpts, diffUseNumberOpts, diffUseInt64Opts} {
			checkAnyFourWay(t, s, af)
		}
		checkRawEnvelopeFourWay(t, s, diffStdOpts)
		checkRawEnvelopeFourWay(t, s, diffNoValValidOpts)
	}
}

func FuzzFiveWayDifferential(f *testing.F) {
	for idx, s := range diffSeedCorpus() {
		f.Add([]byte(s), uint8(idx))
	}
	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		if len(data) > 64<<10 {
			return
		}
		s := string(data)
		switch mode % 6 {
		case 0:
			checkGenericFiveWay[diffFlatStruct](t, s, diffStdOpts, true, true)
			checkGenericFiveWay[diffRichStruct](t, s, diffStdOpts, false, false)
		case 1:
			checkGenericFiveWay[diffFlatStruct](t, s, diffStdDisUnknown, true, true)
			checkGenericFiveWay[diffRichStruct](t, s, diffStdDisUnknown, false, false)
		case 2:
			checkGenericFiveWay[diffFlatStruct](t, s, diffCaseSensOpts, false, true)
			checkGenericFiveWay[diffRichStruct](t, s, diffCaseSensOpts, false, false)
			checkGenericFiveWay[diffFlatStruct](t, s, diffStrictURCOpts, false, true)
			checkGenericFiveWay[diffRichStruct](t, s, diffStrictURCOpts, false, false)
		case 3:
			checkGenericFiveWay[diffFlatStruct](t, s, diffFastCopyOpts, false, true)
			checkGenericFiveWay[diffRichStruct](t, s, diffFastCopyOpts, false, false)
			if json.Valid(data) {
				checkGenericFiveWay[diffFlatStruct](t, s, diffNoValValidOpts, false, true)
				checkGenericFiveWay[diffRichStruct](t, s, diffNoValValidOpts, false, false)
			}
		case 4:
			checkAnyFourWay(t, s, diffStdOpts)
			checkAnyFourWay(t, s, diffUseNumberOpts)
			checkAnyFourWay(t, s, diffUseInt64Opts)
		case 5:
			checkRawEnvelopeFourWay(t, s, diffStdOpts)
			checkRawEnvelopeFourWay(t, s, diffNoValValidOpts)
		}
	})
}
