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
	"errors"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/decoder/consts"
)

func f6StrOverlapsSlice(s string, b []byte) bool {
	if len(s) == 0 || cap(b) == 0 {
		return false
	}
	full := b[:cap(b)]
	p := uintptr(unsafe.Pointer(unsafe.StringData(s)))
	lo := uintptr(unsafe.Pointer(unsafe.SliceData(full)))
	hi := lo + uintptr(len(full))
	return p >= lo && p < hi
}

func f6ExtractErrSrcPos(err error) (src string, pos int, kind string, ok bool) {
	if err == nil {
		return "", 0, "", false
	}
	var sv SyntaxError
	if errors.As(err, &sv) {
		return sv.Src, sv.Pos, "syntax", true
	}
	var sp *SyntaxError
	if errors.As(err, &sp) && sp != nil {
		return sp.Src, sp.Pos, "syntax", true
	}
	var mv MismatchTypeError
	if errors.As(err, &mv) {
		return mv.Src, mv.Pos, "mismatch", true
	}
	var mp *MismatchTypeError
	if errors.As(err, &mp) && mp != nil {
		return mp.Src, mp.Pos, "mismatch", true
	}
	return "", 0, "", false
}

type f6SentinelCB struct {
	FailErr error
	Seen    string
}

func (c *f6SentinelCB) UnmarshalJSON(b []byte) error {
	c.Seen = string(b)
	return c.FailErr
}

// TestF6_StreamSessionParkCleanupMatrix covers all 7 lifecycle cases F6-T1..F6-T7 (§9.3)
// using directly controlled same-session prefilled buffers (zero io.Reader timing luck).
func TestF6_StreamSessionParkCleanupMatrix(t *testing.T) {
	noopRefill := func(_ unsafe.Pointer, _ int) (bool, error) {
		return true, nil
	}

	makeEscStringJSON := func(pairs int) (rawJSON string, wantDecoded string) {
		rawJSON = `"` + strings.Repeat(`a\n`, pairs) + `"`
		wantDecoded = strings.Repeat("a\n", pairs)
		return rawJSON, wantDecoded
	}

	// F6-T1: >64 KiB escaped output + buffered next value -> immediate nil/cap<=64KiB before next Begin/Reset.
	t.Run("F6-T1_OversizedEscapedParkedImmediatelyTrimmed", func(t *testing.T) {
		bigJSON, wantBig := makeEscStringJSON(40 << 10) // 80 KiB decoded (> 64 KiB)
		nextJSON := ` {"next":42}`
		buf := []byte(bigJSON + nextJSON)
		scanp := 0
		var sess *StreamSession
		defer func() { FreeStreamSession(sess) }()

		var first string
		if err := DecodeStream(&buf, &scanp, 0, &first, &sess, nil, noopRefill); err != nil {
			t.Fatalf("first DecodeStream failed: %v", err)
		}
		if first != wantBig {
			t.Fatalf("first decoded string mismatch: len=%d want=%d", len(first), len(wantBig))
		}
		if scanp != len(bigJSON) {
			t.Fatalf("unexpected scanp=%d want=%d", scanp, len(bigJSON))
		}
		// Immediately after return (parked, BEFORE next Begin/Reset):
		if sess.unesc != nil || cap(sess.unesc) > maxRetainedStreamUnescBytes {
			t.Fatalf("F6-T1 failed: parked StreamSession retained sess.unesc cap=%d (>%d)",
				cap(sess.unesc), maxRetainedStreamUnescBytes)
		}
		if sess.m.Unesc != nil || sess.m.Nodes != nil || sess.ctx.Doc.unesc != nil {
			t.Fatalf("F6-T1 failed: parked StreamSession leaked borrows")
		}
		// Verify buffered second value still decodes cleanly!
		var second map[string]int
		if err := DecodeStream(&buf, &scanp, 0, &second, &sess, nil, noopRefill); err != nil {
			t.Fatalf("second DecodeStream failed: %v", err)
		}
		if second["next"] != 42 || scanp != len(buf) {
			t.Fatalf("second value mismatch: %+v scanp=%d/%d", second, scanp, len(buf))
		}
	})

	// F6-T2: 64 KiB boundary & small scratch reuse (no unnecessary reallocation for small streams).
	t.Run("F6-T2_64KiBBoundaryAndSmallScratchReuse", func(t *testing.T) {
		// Small escaped stream: backing slice must be retained and reused across consecutive values.
		smallJSON, wantSmall := makeEscStringJSON(200) // 400 B decoded
		buf := []byte(smallJSON + " " + smallJSON + " " + smallJSON)
		scanp := 0
		var sess *StreamSession
		defer func() { FreeStreamSession(sess) }()

		var s1, s2, s3 string
		if err := DecodeStream(&buf, &scanp, 0, &s1, &sess, nil, noopRefill); err != nil || s1 != wantSmall {
			t.Fatalf("s1 failed: err=%v", err)
		}
		if sess.unesc == nil || len(sess.unesc) != 0 || cap(sess.unesc) == 0 || cap(sess.unesc) > maxRetainedStreamUnescBytes {
			t.Fatalf("small scratch should be retained at len=0 cap<=64KiB, got len=%d cap=%d", len(sess.unesc), cap(sess.unesc))
		}
		basePtr1 := unsafe.SliceData(sess.unesc[:cap(sess.unesc)])
		if err := DecodeStream(&buf, &scanp, 0, &s2, &sess, nil, noopRefill); err != nil || s2 != wantSmall {
			t.Fatalf("s2 failed: err=%v", err)
		}
		basePtr2 := unsafe.SliceData(sess.unesc[:cap(sess.unesc)])
		if basePtr1 != basePtr2 {
			t.Fatalf("small scratch backing was reallocated instead of reused: %p != %p", basePtr1, basePtr2)
		}
		if err := DecodeStream(&buf, &scanp, 0, &s3, &sess, nil, noopRefill); err != nil || s3 != wantSmall {
			t.Fatalf("s3 failed: err=%v", err)
		}

		// Exact 64 KiB cap boundary check on parkCleanup():
		sess.unesc = make([]byte, 128, maxRetainedStreamUnescBytes)
		exactPtr := unsafe.SliceData(sess.unesc)
		sess.parkCleanup()
		if sess.unesc == nil || len(sess.unesc) != 0 || cap(sess.unesc) != maxRetainedStreamUnescBytes || unsafe.SliceData(sess.unesc[:1]) != exactPtr {
			t.Fatalf("exact 64KiB boundary should retain backing: len=%d cap=%d", len(sess.unesc), cap(sess.unesc))
		}
		sess.unesc = make([]byte, 128, maxRetainedStreamUnescBytes+1)
		sess.parkCleanup()
		if sess.unesc != nil || cap(sess.unesc) != 0 {
			t.Fatalf("64KiB+1 boundary should release backing, got cap=%d", cap(sess.unesc))
		}
	})

	// F6-T3: Same session large -> small -> large -> small multi-value stream stability.
	t.Run("F6-T3_LargeSmallLargeMultiValueSameSession", func(t *testing.T) {
		big1JSON, wantBig1 := makeEscStringJSON(36 << 10) // 72 KiB
		small1JSON, wantSmall1 := makeEscStringJSON(100)  // 200 B
		big2JSON, wantBig2 := makeEscStringJSON(44 << 10) // 88 KiB
		small2JSON, wantSmall2 := makeEscStringJSON(50)   // 100 B
		buf := []byte(big1JSON + "\n" + small1JSON + "\n" + big2JSON + "\n" + small2JSON)
		scanp := 0
		var sess *StreamSession
		defer func() { FreeStreamSession(sess) }()

		var g1, g2, g3, g4 string
		if err := DecodeStream(&buf, &scanp, 0, &g1, &sess, nil, noopRefill); err != nil || g1 != wantBig1 {
			t.Fatalf("g1 failed: err=%v", err)
		}
		if cap(sess.unesc) != 0 {
			t.Fatalf("after big1 expected trimmed unesc, got cap=%d", cap(sess.unesc))
		}
		if err := DecodeStream(&buf, &scanp, 0, &g2, &sess, nil, noopRefill); err != nil || g2 != wantSmall1 {
			t.Fatalf("g2 failed: err=%v", err)
		}
		if cap(sess.unesc) == 0 || cap(sess.unesc) > maxRetainedStreamUnescBytes {
			t.Fatalf("after small1 expected bounded small unesc, got cap=%d", cap(sess.unesc))
		}
		if err := DecodeStream(&buf, &scanp, 0, &g3, &sess, nil, noopRefill); err != nil || g3 != wantBig2 {
			t.Fatalf("g3 failed: err=%v", err)
		}
		if cap(sess.unesc) != 0 {
			t.Fatalf("after big2 expected trimmed unesc, got cap=%d", cap(sess.unesc))
		}
		if err := DecodeStream(&buf, &scanp, 0, &g4, &sess, nil, noopRefill); err != nil || g4 != wantSmall2 {
			t.Fatalf("g4 failed: err=%v", err)
		}
		if g1 != wantBig1 || g2 != wantSmall1 || g3 != wantBig2 || g4 != wantSmall2 || scanp != len(buf) {
			t.Fatalf("multi-value results corrupted or cursor wrong (scanp=%d/%d)", scanp, len(buf))
		}
	})

	// F6-T4: First value FromDom materialization error (MismatchTypeError + custom sentinel callback error)
	// with buffered next value -> scratch released, error source detached, sentinel identity preserved.
	t.Run("F6-T4_FromDomErrorsReleaseScratchAndPreserveIdentity", func(t *testing.T) {
		type errStruct struct {
			Big string `json:"big"`
			Num int    `json:"num"`
		}
		bigJSON, _ := makeEscStringJSON(40 << 10)
		val1 := `{"big":` + bigJSON + `,"num":"not_an_int"}`
		buf := []byte(val1 + ` {"next":1}`)
		scanp := 0
		var sess *StreamSession
		defer func() { FreeStreamSession(sess) }()

		var dst errStruct
		err := DecodeStream(&buf, &scanp, 0, &dst, &sess, nil, noopRefill)
		var mErrVal MismatchTypeError
		var mErrPtr *MismatchTypeError
		if !errors.As(err, &mErrVal) && !errors.As(err, &mErrPtr) {
			t.Fatalf("expected MismatchTypeError, got %T (%v)", err, err)
		}
		if cap(sess.unesc) > maxRetainedStreamUnescBytes {
			t.Fatalf("FromDom mismatch error leaked oversized unesc cap=%d", cap(sess.unesc))
		}
		// Assert Src does NOT overlap exact &buf backing, then overwrite buf and verify Src, Pos, and Error().
		srcBefore, posBefore, kindBefore, okBefore := f6ExtractErrSrcPos(err)
		if !okBefore || kindBefore != "mismatch" || len(srcBefore) == 0 {
			t.Fatalf("expected detached MismatchTypeError with non-empty Src, got ok=%v kind=%q len(Src)=%d err=%v", okBefore, kindBefore, len(srcBefore), err)
		}
		if f6StrOverlapsSlice(srcBefore, buf) {
			t.Fatalf("MismatchTypeError.Src unexpectedly aliased exact stream &buf backing")
		}
		errTextBefore := strings.Clone(err.Error())
		wantSrc := strings.Clone(srcBefore)
		copy(buf, bytes.Repeat([]byte{'X'}, len(buf)))
		runtime.GC()
		srcAfter, posAfter, _, _ := f6ExtractErrSrcPos(err)
		if err.Error() != errTextBefore || srcAfter != wantSrc || posAfter != posBefore {
			t.Fatalf("MismatchTypeError source was not detached from exact stream &buf: pos=%d/%d srcMatch=%v errMatch=%v",
				posAfter, posBefore, srcAfter == wantSrc, err.Error() == errTextBefore)
		}

		// Deterministic SyntaxError detachment on exact &buf backing (N2 closeout polish).
		synBuf := []byte(`{"big":` + bigJSON + `,"bad":} {"next":3}`)
		synScanp := 0
		var synSess *StreamSession
		defer func() { FreeStreamSession(synSess) }()
		var synDst errStruct
		synErr := DecodeStream(&synBuf, &synScanp, 0, &synDst, &synSess, nil, noopRefill)
		synSrcBefore, synPosBefore, synKind, synOK := f6ExtractErrSrcPos(synErr)
		if !synOK || synKind != "syntax" || len(synSrcBefore) == 0 {
			t.Fatalf("expected SyntaxError with non-empty Src, got %T (%v)", synErr, synErr)
		}
		if f6StrOverlapsSlice(synSrcBefore, synBuf) {
			t.Fatalf("SyntaxError.Src unexpectedly aliased exact stream &buf backing")
		}
		wantSynText := strings.Clone(synErr.Error())
		wantSynSrc := strings.Clone(synSrcBefore)
		copy(synBuf, bytes.Repeat([]byte{'Y'}, len(synBuf)))
		runtime.GC()
		synSrcAfter, synPosAfter, _, _ := f6ExtractErrSrcPos(synErr)
		if synErr.Error() != wantSynText || synSrcAfter != wantSynSrc || synPosAfter != synPosBefore {
			t.Fatalf("SyntaxError source was not detached from exact stream &buf: pos=%d/%d srcMatch=%v errMatch=%v",
				synPosAfter, synPosBefore, synSrcAfter == wantSynSrc, synErr.Error() == wantSynText)
		}

		// Callback sentinel error identity preserved + oversized scratch released!
		type cbStruct struct {
			Big string       `json:"big"`
			CB  f6SentinelCB `json:"cb"`
		}
		sentinel := errors.New("f6-custom-callback-failure")
		buf2 := []byte(`{"big":` + bigJSON + `,"cb":{"k":1}} {"next":2}`)
		scanp2 := 0
		dst2 := cbStruct{CB: f6SentinelCB{FailErr: sentinel}}
		err2 := DecodeStream(&buf2, &scanp2, 0, &dst2, &sess, nil, noopRefill)
		if !errors.Is(err2, sentinel) {
			t.Fatalf("expected errors.Is(err2, sentinel), got %T (%v)", err2, err2)
		}
		if cap(sess.unesc) > maxRetainedStreamUnescBytes {
			t.Fatalf("FromDom callback error leaked oversized unesc cap=%d", cap(sess.unesc))
		}
	})

	// F6-T5: UTF-8 repair path on >64 KiB escaped string + buffered next value -> clean repair, trimmed scratch, next value intact.
	t.Run("F6-T5_UTF8RepairPathTrimsScratchAndPreservesNext", func(t *testing.T) {
		rawBad := `"` + strings.Repeat(`a\n`, 36<<10) + "\xff" + `"`
		wantRepaired := strings.Repeat("a\n", 36<<10) + "\ufffd"
		buf := []byte(rawBad + ` {"after":99}`)
		scanp := 0
		var sess *StreamSession
		defer func() { FreeStreamSession(sess) }()

		var s1 string
		if err := DecodeStream(&buf, &scanp, 1<<consts.F_validate_string, &s1, &sess, nil, noopRefill); err != nil {
			t.Fatalf("UTF-8 repair DecodeStream failed: %v", err)
		}
		if s1 != wantRepaired || scanp != len(rawBad) {
			t.Fatalf("UTF-8 repair result mismatch: len=%d want=%d scanp=%d/%d", len(s1), len(wantRepaired), scanp, len(rawBad))
		}
		if cap(sess.unesc) > maxRetainedStreamUnescBytes {
			t.Fatalf("UTF-8 repair path left oversized unesc cap=%d", cap(sess.unesc))
		}
		var next map[string]int
		if err := DecodeStream(&buf, &scanp, 1<<consts.F_validate_string, &next, &sess, nil, noopRefill); err != nil || next["after"] != 99 {
			t.Fatalf("post-repair second value failed: err=%v next=%+v", err, next)
		}
	})

	// F6-T6 & F6-T7: Returned large string immutability across buffer mutation, re-decode, and FreeStreamSession pool recycle.
	t.Run("F6-T6_T7_ReturnedStringImmutableAcrossPoolRecycle", func(t *testing.T) {
		bigJSON, wantBig := makeEscStringJSON(40 << 10)
		buf := []byte(bigJSON)
		scanp := 0
		var sess *StreamSession

		var saved string
		if err := DecodeStream(&buf, &scanp, 0, &saved, &sess, nil, noopRefill); err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		// Overwrite caller buffer and return session to pool, then hammer pool with distinct escaped strings!
		copy(buf, bytes.Repeat([]byte{'Z'}, len(buf)))
		FreeStreamSession(sess)
		sess = nil

		otherJSON, _ := makeEscStringJSON(40 << 10)
		otherJSON = strings.ReplaceAll(otherJSON, "a", "q")
		for i := 0; i < 8; i++ {
			b2 := []byte(otherJSON)
			sp2 := 0
			var s2 *StreamSession
			var tmp string
			if err := DecodeStream(&b2, &sp2, 0, &tmp, &s2, nil, noopRefill); err != nil {
				t.Fatalf("recycle iter %d failed: %v", i, err)
			}
			FreeStreamSession(s2)
		}
		if saved != wantBig {
			t.Fatalf("earlier returned large string was mutated after buffer overwrite & FreeStreamSession recycle")
		}
	})
}
