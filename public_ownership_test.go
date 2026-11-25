// Copyright 2026 ByteDance Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sonic

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

type n2SlidingChunkReader struct {
	chunks [][]byte
	idx    int
	off    int
	buf    []byte
}

func (r *n2SlidingChunkReader) Read(p []byte) (int, error) {
	if len(r.buf) > 0 {
		for i := range r.buf {
			r.buf[i] = 'Z'
		}
	}
	for r.idx < len(r.chunks) && r.off >= len(r.chunks[r.idx]) {
		r.idx++
		r.off = 0
	}
	if r.idx >= len(r.chunks) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	rem := r.chunks[r.idx][r.off:]
	if len(rem) > len(p) {
		rem = rem[:len(p)]
	}
	if cap(r.buf) < len(rem) {
		r.buf = make([]byte, len(rem))
	}
	r.buf = r.buf[:len(rem)]
	copy(r.buf, rem)
	n := copy(p, r.buf)
	r.off += n
	return n, nil
}

func n2PtrInStringRange(ptr uintptr, base string) bool {
	if len(base) == 0 || ptr == 0 {
		return false
	}
	lo := uintptr(unsafe.Pointer(unsafe.StringData(base)))
	return ptr >= lo && ptr < lo+uintptr(len(base))
}

func n2ExtractSonicErrSource(err error) (src string, pos int, ok bool) {
	if err == nil {
		return "", 0, false
	}
	rv := reflect.ValueOf(err)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return "", 0, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return "", 0, false
	}
	fSrc := rv.FieldByName("Src")
	fPos := rv.FieldByName("Pos")
	if fSrc.IsValid() && fSrc.Kind() == reflect.String && fPos.IsValid() && fPos.Kind() == reflect.Int {
		return fSrc.String(), int(fPos.Int()), true
	}
	return "", 0, false
}

// TestN2_PublicAPIOwnershipAndErrorSourceContracts independently verifies the
// public ownership & retention contracts documented in README.md (String Ownership and CopyString):
//   - Unmarshal([]byte) detaches strings, RawMessage, NoCopyRawMessage, and error sources from caller buf
//   - UnmarshalString(string) honors CopyString=false zero-copy vs CopyString=true detachment on plain valid UTF-8
//   - StreamDecoder detaches returned values and error sources from sliding/reused stream buffers
func TestN2_PublicAPIOwnershipAndErrorSourceContracts(t *testing.T) {
	type payload struct {
		ID     int              `json:"id"`
		Name   string           `json:"name"`
		Raw    json.RawMessage  `json:"raw"`
		NoCopy NoCopyRawMessage `json:"nocopy"`
	}

	// 1. Unmarshal([]byte): plain valid UTF-8 (no escape, no UTF-8 repair) MUST detach all outputs from caller buf!
	t.Run("UnmarshalBytes_PlainUTF8DetachedOnCallerMutation", func(t *testing.T) {
		const rawJSON = `{"id":7,"name":"plain_ascii_value_0123456789","raw":{"k":1},"nocopy":{"nc":2}}`
		for _, tc := range []struct {
			name string
			api  API
		}{
			{"ConfigDefault", ConfigDefault},
			{"ConfigStd", ConfigStd},
			{"ConfigCopyString", Config{CopyString: true}.Froze()},
		} {
			t.Run(tc.name, func(t *testing.T) {
				buf := []byte(rawJSON)
				var dst payload
				if err := tc.api.Unmarshal(buf, &dst); err != nil {
					t.Fatalf("Unmarshal failed: %v", err)
				}
				// Mutate caller []byte after Unmarshal returns (explicitly permitted for Unmarshal([]byte)).
				copy(buf, bytes.Repeat([]byte{'X'}, len(buf)))
				if dst.ID != 7 || dst.Name != "plain_ascii_value_0123456789" {
					t.Fatalf("caller buf mutation corrupted decoded fields: %+v", dst)
				}
				if string(dst.Raw) != `{"k":1}` {
					t.Fatalf("caller buf mutation corrupted json.RawMessage: %q", string(dst.Raw))
				}
				if string(dst.NoCopy) != `{"nc":2}` {
					t.Fatalf("caller buf mutation corrupted NoCopyRawMessage under Unmarshal([]byte): %q", string(dst.NoCopy))
				}
			})
		}
	})

	// 2. Unmarshal([]byte) errors (syntax & mismatch, both plain UTF-8 & invalid UTF-8): error source MUST stay detached!
	t.Run("UnmarshalBytes_ErrorSourceDetachedOnCallerMutation", func(t *testing.T) {
		errCases := []struct {
			name string
			src  string
		}{
			{"PlainSyntaxErr", `{"id":7,"name":"plain_before_err","bad":}`},
			{"PlainMismatchErr", `{"id":"not_an_int","name":"plain_mismatch"}`},
			{"UTF8RepairSyntaxErr", "{\"id\":7,\"name\":\"a\xffb\",\"bad\":}"},
			{"UTF8RepairMismatchErr", "{\"id\":\"not_an_int\",\"name\":\"a\xffb\"}"},
		}
		for _, tc := range errCases {
			t.Run(tc.name, func(t *testing.T) {
				buf := []byte(tc.src)
				var dst payload
				err := ConfigDefault.Unmarshal(buf, &dst)
				if err == nil {
					t.Fatalf("expected error on %q, got nil", tc.src)
				}
				wantText := strings.Clone(err.Error())
				wantSrc, wantPos, hasSonicSrc := n2ExtractSonicErrSource(err)
				wantSrcClone := strings.Clone(wantSrc)

				// Mutate caller buffer after Unmarshal returns!
				copy(buf, bytes.Repeat([]byte{'X'}, len(buf)))
				runtime.GC()

				if err.Error() != wantText {
					t.Fatalf("caller buf mutation corrupted err.Error():\n  got:  %q\n  want: %q", err.Error(), wantText)
				}
				if APIKind == UseSonicJSON {
					if !hasSonicSrc {
						t.Fatalf("expected SyntaxError or MismatchTypeError on UseSonicJSON, got %T (%v)", err, err)
					}
					gotSrc, gotPos, _ := n2ExtractSonicErrSource(err)
					if gotPos != wantPos || gotSrc != wantSrcClone {
						t.Fatalf("caller buf mutation corrupted detached error source: got=(pos=%d src=%q) want=(pos=%d src=%q)",
							gotPos, gotSrc, wantPos, wantSrcClone)
					}
				}
			})
		}
	})

	// 3. UnmarshalFromString(immutable string): plain valid UTF-8 distinguishes zero-copy borrow vs CopyString detachment.
	t.Run("UnmarshalString_PlainUTF8BorrowVsCopyString", func(t *testing.T) {
		plainSrc := strings.Clone(`{"id":9,"name":"immutable_string_token_99","raw":{"r":1},"nocopy":{"n":2}}`)
		var dstBorrow, dstCopy payload
		if err := UnmarshalString(plainSrc, &dstBorrow); err != nil {
			t.Fatalf("UnmarshalString failed: %v", err)
		}
		if err := (Config{CopyString: true}.Froze()).UnmarshalFromString(plainSrc, &dstCopy); err != nil {
			t.Fatalf("CopyString.UnmarshalFromString failed: %v", err)
		}
		if dstBorrow.Name != "immutable_string_token_99" || string(dstBorrow.Raw) != `{"r":1}` || string(dstBorrow.NoCopy) != `{"n":2}` {
			t.Fatalf("unexpected UnmarshalString borrow values: %+v", dstBorrow)
		}
		if dstCopy.Name != "immutable_string_token_99" || string(dstCopy.Raw) != `{"r":1}` || string(dstCopy.NoCopy) != `{"n":2}` {
			t.Fatalf("unexpected UnmarshalString copy values: %+v", dstCopy)
		}
		// json.RawMessage must ALWAYS be detached regardless of CopyString.
		if n2PtrInStringRange(uintptr(unsafe.Pointer(unsafe.SliceData(dstBorrow.Raw))), plainSrc) {
			t.Fatal("json.RawMessage unexpectedly aliased immutable input string")
		}
		if n2PtrInStringRange(uintptr(unsafe.Pointer(unsafe.SliceData(dstCopy.Raw))), plainSrc) {
			t.Fatal("CopyString json.RawMessage unexpectedly aliased immutable input string")
		}
		// CopyString: true must ALWAYS detach Name and NoCopyRawMessage.
		if n2PtrInStringRange(uintptr(unsafe.Pointer(unsafe.StringData(dstCopy.Name))), plainSrc) {
			t.Fatal("CopyString=true Name unexpectedly aliased immutable input string")
		}
		if n2PtrInStringRange(uintptr(unsafe.Pointer(unsafe.SliceData(dstCopy.NoCopy))), plainSrc) {
			t.Fatal("CopyString=true NoCopyRawMessage unexpectedly aliased immutable input string")
		}
		// On UseSonicJSON with CopyString=false, plain valid UTF-8 Name and NoCopyRawMessage borrow immutable s.
		if APIKind == UseSonicJSON {
			if !n2PtrInStringRange(uintptr(unsafe.Pointer(unsafe.StringData(dstBorrow.Name))), plainSrc) {
				t.Fatal("UseSonicJSON ConfigDefault.UnmarshalString Name did not zero-copy borrow plain immutable string")
			}
			if !n2PtrInStringRange(uintptr(unsafe.Pointer(unsafe.SliceData(dstBorrow.NoCopy))), plainSrc) {
				t.Fatal("UseSonicJSON ConfigDefault.UnmarshalString NoCopyRawMessage did not borrow immutable string")
			}
		}
	})

	// 4. StreamDecoder: values AND error sources must detach from sliding/reused reader buffers (StreamDecoder detachment contract).
	t.Run("StreamDecoder_SlidingBufferValueAndErrorSourceDetachment", func(t *testing.T) {
		// 4.0 Self-test n2SlidingChunkReader short-read semantics (len(p) < len(chunk))
		// so stdlib/jsonv2 tail buffers shorter than a chunk never lose bytes.
		srCheck := &n2SlidingChunkReader{chunks: [][]byte{[]byte("abcdefghij"), []byte("0123456789")}}
		var gotAll []byte
		small := make([]byte, 3)
		for {
			n, err := srCheck.Read(small)
			gotAll = append(gotAll, small[:n]...)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("short-read self-test error: %v", err)
			}
		}
		if string(gotAll) != "abcdefghij0123456789" {
			t.Fatalf("n2SlidingChunkReader short-read lost bytes: got=%q", string(gotAll))
		}

		// 4a. Multi-value stream over self-overwriting sliding chunk reader.
		r := &n2SlidingChunkReader{
			chunks: [][]byte{
				[]byte(`{"id":1,"name":"first_plain_val","raw":{"a":1}} `),
				[]byte(`{"id":2,"name":"second_escaped\nval","raw":{"b":2}} `),
				[]byte(`{"id":3,"name":"third_overwrite_buffer_padding_0123456789","raw":{"c":3}}`),
			},
		}
		dec := ConfigDefault.NewDecoder(r)
		var v1, v2, v3 payload
		if err := dec.Decode(&v1); err != nil {
			t.Fatalf("stream value 1 failed: %v", err)
		}
		if err := dec.Decode(&v2); err != nil {
			t.Fatalf("stream value 2 failed: %v", err)
		}
		if err := dec.Decode(&v3); err != nil {
			t.Fatalf("stream value 3 failed: %v", err)
		}
		runtime.GC()
		if v1.ID != 1 || v1.Name != "first_plain_val" || string(v1.Raw) != `{"a":1}` {
			t.Fatalf("stream sliding buffer corrupted value 1: %+v", v1)
		}
		if v2.ID != 2 || v2.Name != "second_escaped\nval" || string(v2.Raw) != `{"b":2}` {
			t.Fatalf("stream sliding buffer corrupted value 2: %+v", v2)
		}
		if v3.ID != 3 || v3.Name != "third_overwrite_buffer_padding_0123456789" || string(v3.Raw) != `{"c":3}` {
			t.Fatalf("stream sliding buffer corrupted value 3: %+v", v3)
		}

		// 4b. Public StreamDecoder error-source surface check across reader scratch overwrite and fresh decoders.
		// Note: sr.Read overwrites the caller reader's scratch (not deterministically the decoder's pooled self.buf);
		// deterministic exact-&buf non-overlap and overwrite proofs for both SyntaxError and MismatchTypeError
		// are enforced in internal/decoder/optdec/f6_stream_park_cleanup_test.go (F6-T4).
		for _, tc := range []struct {
			name   string
			chunks [][]byte
		}{
			{"StreamSyntaxErr", [][]byte{[]byte(`{"id":1,"name":"stream_syn","bad":}`), []byte(` {"id":2}`)}},
			{"StreamMismatchErr", [][]byte{[]byte(`{"id":"bad_int","name":"stream_mis"}`), []byte(` {"id":2,"name":"next"}`)}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				sr := &n2SlidingChunkReader{chunks: tc.chunks}
				sd := ConfigDefault.NewDecoder(sr)
				var dst payload
				err := sd.Decode(&dst)
				if err == nil {
					t.Fatalf("expected stream error in %s", tc.name)
				}
				wantText := strings.Clone(err.Error())
				wantSrc, wantPos, hasSonicSrc := n2ExtractSonicErrSource(err)
				wantSrcClone := strings.Clone(wantSrc)

				// Overwrite caller-side reader scratch and exercise fresh StreamDecoder allocations.
				_, _ = sr.Read(make([]byte, 64))
				for i := 0; i < 8; i++ {
					var tmp payload
					_ = ConfigDefault.NewDecoder(strings.NewReader(`{"id":99,"name":"recycle\nstream\tscratch"}`)).Decode(&tmp)
				}
				runtime.GC()

				if err.Error() != wantText {
					t.Fatalf("stream buffer reuse corrupted err.Error():\n  got:  %q\n  want: %q", err.Error(), wantText)
				}
				if APIKind == UseSonicJSON {
					if !hasSonicSrc {
						t.Fatalf("expected SyntaxError or MismatchTypeError on UseSonicJSON, got %T (%v)", err, err)
					}
					gotSrc, gotPos, _ := n2ExtractSonicErrSource(err)
					if gotPos != wantPos || gotSrc != wantSrcClone {
						t.Fatalf("stream buffer reuse corrupted detached error source: got=(pos=%d src=%q) want=(pos=%d src=%q)",
							gotPos, gotSrc, wantPos, wantSrcClone)
					}
				}
			})
		}
	})
}
