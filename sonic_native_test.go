//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package sonic

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/decoder"
)

func TestCopyUnmarshalErrorSourceDetachesAllErrorKinds(t *testing.T) {
	orig := `{"age":"not_an_int","num":"bad_num"}`
	buf := []byte(orig)
	aliasFull := unsafe.String(&buf[0], len(buf))
	aliasSub := unsafe.String(&buf[8], 10) // "not_an_int"

	valMismatch := copyUnmarshalErrorSource(decoder.MismatchTypeError{
		Pos:  7,
		Src:  aliasFull,
		Type: reflect.TypeOf(0),
	}, buf)
	ptrSyntax := copyUnmarshalErrorSource(&decoder.SyntaxError{
		Pos: 7,
		Src: aliasFull,
		Msg: "boom",
	}, buf)
	unmarshalType := copyUnmarshalErrorSource(&json.UnmarshalTypeError{
		Value: aliasSub,
		Field: aliasSub,
		Type:  reflect.TypeOf(json.Number("")),
	}, buf)
	numErr := copyUnmarshalErrorSource(&strconv.NumError{
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
}
