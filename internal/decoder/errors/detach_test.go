package errors

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
	"unsafe"
)

func TestDetachSourceClonesBorrowedAndPreservesUserErrors(t *testing.T) {
	buf := []byte(`{"num":12345,"bad":true}`)
	borrowedFull := unsafe.String(unsafe.SliceData(buf), len(buf))
	borrowedField := borrowedFull[2:5] // "num"
	borrowedNum := borrowedFull[7:12]  // "12345"

	synVal := DetachSource(SyntaxError{Pos: 1, Src: borrowedFull}, buf).(SyntaxError)
	synPtr := DetachSource(&SyntaxError{Pos: 1, Src: borrowedFull}, buf).(*SyntaxError)
	misVal := DetachSource(MismatchTypeError{Pos: 7, Src: borrowedFull, Type: reflect.TypeOf(0)}, buf).(MismatchTypeError)
	misPtr := DetachSource(&MismatchTypeError{Pos: 7, Src: borrowedFull, Type: reflect.TypeOf(0)}, buf).(*MismatchTypeError)
	ute := DetachSource(&json.UnmarshalTypeError{Value: borrowedNum, Field: borrowedField, Type: reflect.TypeOf(0)}, buf).(*json.UnmarshalTypeError)
	numErr := DetachSource(&strconv.NumError{Func: "ParseInt", Num: borrowedNum, Err: strconv.ErrRange}, buf).(*strconv.NumError)

	userSrc := `{"external":1}`
	userSyn := SyntaxError{Pos: 2, Src: userSrc}
	detachedUser := DetachSource(userSyn, buf).(SyntaxError)

	// Mutate backing buffer completely to prove detachment.
	for i := range buf {
		buf[i] = 'X'
	}

	if synVal.Src != `{"num":12345,"bad":true}` || synPtr.Src != `{"num":12345,"bad":true}` {
		t.Fatalf("SyntaxError.Src aliased mutated buffer: val=%q ptr=%q", synVal.Src, synPtr.Src)
	}
	if misVal.Src != `{"num":12345,"bad":true}` || misPtr.Src != `{"num":12345,"bad":true}` {
		t.Fatalf("MismatchTypeError.Src aliased mutated buffer: val=%q ptr=%q", misVal.Src, misPtr.Src)
	}
	if ute.Field != "num" || ute.Value != "12345" {
		t.Fatalf("UnmarshalTypeError aliased mutated buffer: field=%q value=%q", ute.Field, ute.Value)
	}
	if numErr.Num != "12345" {
		t.Fatalf("NumError.Num aliased mutated buffer: %q", numErr.Num)
	}
	if unsafe.StringData(detachedUser.Src) != unsafe.StringData(userSrc) || detachedUser.Src != userSrc {
		t.Fatalf("user callback SyntaxError should remain untouched: %q", detachedUser.Src)
	}
}
