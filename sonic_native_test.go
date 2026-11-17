//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package sonic

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/decoder"
	decerrors "github.com/bytedance/sonic/internal/decoder/errors"
)

func TestCopyUnmarshalErrorSourceDetachesAllErrorKinds(t *testing.T) {
	orig := `{"age":"not_an_int","num":"bad_num"}`
	buf := []byte(orig)
	aliasFull := unsafe.String(&buf[0], len(buf))
	aliasSub := unsafe.String(&buf[8], 10) // "not_an_int"

	valMismatch := decerrors.DetachSource(decoder.MismatchTypeError{
		Pos:  7,
		Src:  aliasFull,
		Type: reflect.TypeOf(0),
	}, buf)
	ptrSyntax := decerrors.DetachSource(&decoder.SyntaxError{
		Pos: 7,
		Src: aliasFull,
		Msg: "boom",
	}, buf)
	unmarshalType := decerrors.DetachSource(&json.UnmarshalTypeError{
		Value: aliasSub,
		Field: aliasSub,
		Type:  reflect.TypeOf(json.Number("")),
	}, buf)
	numErr := decerrors.DetachSource(&strconv.NumError{
		Func: "ParseInt",
		Num:  aliasSub,
		Err:  strconv.ErrSyntax,
	}, buf)

	for i := range buf {
		buf[i] = 'X'
	}

	if msg := valMismatch.Error(); strings.Contains(msg, "XXXX") || !strings.Contains(msg, "not_an_int") {
		t.Fatalf("value MismatchTypeError still aliased caller buffer: %s", msg)
	}
	if msg := ptrSyntax.Error(); strings.Contains(msg, "XXXX") || !strings.Contains(msg, "not_an_int") {
		t.Fatalf("pointer SyntaxError still aliased caller buffer: %s", msg)
	}
	var ute *json.UnmarshalTypeError
	if !errors.As(unmarshalType, &ute) || ute.Value != "not_an_int" || ute.Field != "not_an_int" {
		t.Fatalf("UnmarshalTypeError still aliased caller buffer: %+v", ute)
	}
	var ne *strconv.NumError
	if !errors.As(numErr, &ne) || ne.Num != "not_an_int" {
		t.Fatalf("strconv.NumError still aliased caller buffer: %+v", ne)
	}

	// End-to-end Unmarshal([]byte) and StreamDecoder error detachment after buffer reuse.
	type target struct {
		Age int `json:"age"`
	}
	liveBuf := []byte(`{"age":"wrong_type"}`)
	var dst target
	uerr := Unmarshal(liveBuf, &dst)
	if uerr == nil {
		t.Fatal("expected Unmarshal error")
	}
	for i := range liveBuf {
		liveBuf[i] = 'Z'
	}
	if strings.Contains(uerr.Error(), "ZZZZ") || !strings.Contains(uerr.Error(), "wrong_type") {
		t.Fatalf("Unmarshal returned error aliasing mutated caller buffer: %s", uerr.Error())
	}

	streamDec := ConfigDefault.NewDecoder(bytes.NewReader([]byte(`{"age":"stream_bad"} {"age":1}`)))
	serr := streamDec.Decode(&dst)
	if serr == nil || !strings.Contains(serr.Error(), "stream_bad") {
		t.Fatalf("unexpected StreamDecoder error: %v", serr)
	}
}
