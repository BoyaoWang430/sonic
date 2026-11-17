//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package kernels

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/bytedance/sonic/internal/rt"
)

// TestScalarVsActiveKernelProtocolEquivalence verifies that every pure-Go scalar
// kernel primitive produces identical outputs, consumed counts, status codes,
// and error offsets as the active architecture kernel across alignments, buffer
// capacities, valid UTF-8/escapes, and malformed inputs (Contract G3 Dimension 9).
func TestScalarVsActiveKernelProtocolEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(20261117))
	alphabet := []byte(" \t\r\n{}[],:\"\\0123456789.eE+-truefalsn<>&#/\x00\x08\x0c\x1f\xc2\xa2\xe4\xb8\xad\xe2\x80\xa8\xe2\x80\xa9\xf0\x9f\x98\x80\xff")

	for trial := 0; trial < 600; trial++ {
		n := rng.Intn(196)
		buf := make([]byte, n)
		for i := range buf {
			buf[i] = alphabet[rng.Intn(len(alphabet))]
		}
		ptr := rt.SliceData(buf)

		if gotA, gotS := space(ptr, n), spaceScalar(ptr, n); gotA != gotS {
			t.Fatalf("trial %d space mismatch: asm=%d scalar=%d", trial, gotA, gotS)
		}
		if gotA, gotS := nonSpaceMask(ptr, minInt(n, 64)), nonSpaceMaskScalar(ptr, minInt(n, 64)); gotA != gotS {
			t.Fatalf("trial %d nonSpaceMask mismatch: asm=%x scalar=%x", trial, gotA, gotS)
		}
		for _, v := range []uint64{0, 1} {
			eA, nsA := traversalMasks(ptr, minInt(n, 64), v)
			eS, nsS := traversalMasksScalar(ptr, minInt(n, 64), v)
			if eA != eS || nsA != nsS {
				t.Fatalf("trial %d traversalMasks(%d) mismatch: asm=(%x,%x) scalar=(%x,%x)", trial, v, eA, nsA, eS, nsS)
			}
		}
		eA, nsA, bA := pathMasks(ptr, minInt(n, 64))
		eS, nsS, bS := pathMasksScalar(ptr, minInt(n, 64))
		if eA != eS || nsA != nsS || bA != bS {
			t.Fatalf("trial %d pathMasks mismatch: asm=(%x,%x,%x) scalar=(%x,%x,%x)", trial, eA, nsA, bA, eS, nsS, bS)
		}

		endA, escA, posA, errA := scanStringRelaxed(ptr, n)
		endS, escS, posS, errS := scanStringRelaxedScalar(ptr, n)
		if endA != endS || escA != escS || posA != posS || errA != errS {
			t.Fatalf("trial %d scanStringRelaxed mismatch: asm=(%d,%d,%d,%v) scalar=(%d,%d,%d,%v)",
				trial, endA, escA, posA, errA, endS, escS, posS, errS)
		}
		endA, escA, posA, errA = scanStringStrict(ptr, n)
		endS, escS, posS, errS = scanStringStrictScalar(ptr, n)
		if endA != endS || escA != escS || posA != posS || errA != errS {
			t.Fatalf("trial %d scanStringStrict mismatch: asm=(%d,%d,%d,%v) scalar=(%d,%d,%d,%v)",
				trial, endA, escA, posA, errA, endS, escS, posS, errS)
		}

		for _, open := range []uint64{'{', '['} {
			cEndA, cErrA := skipContainer(ptr, n, open)
			cEndS, cErrS := skipContainerScalar(ptr, n, open)
			if cEndA != cEndS || cErrA != cErrS {
				t.Fatalf("trial %d skipContainer(%c) mismatch: asm=(%d,%v) scalar=(%d,%v)", trial, byte(open), cEndA, cErrA, cEndS, cErrS)
			}
		}

		for _, mode := range []uint64{0, 1, 2} {
			nEndA, nErrA := numberScan(ptr, n, mode)
			nEndS, nErrS := numberScanScalar(ptr, n, mode)
			if nEndA != nEndS || nErrA != nErrS {
				t.Fatalf("trial %d numberScan(mode=%d) mismatch: asm=(%d,%v) scalar=(%d,%v)", trial, mode, nEndA, nErrA, nEndS, nErrS)
			}
		}

		for _, mode := range []uint64{0, 1, 2, 3, 4, 6, 12, 14} {
			inA := append([]byte(nil), buf...)
			inS := append([]byte(nil), buf...)
			dEndA, wA, sEscA, dPosA, codeA := stringDecode(rt.SliceData(inA), n, rt.SliceData(inA), mode)
			dEndS, wS, sEscS, dPosS, codeS := stringDecodeScalar(rt.SliceData(inS), n, rt.SliceData(inS), mode)
			if dEndA != dEndS || wA != wS || (sEscA != 0) != (sEscS != 0) || dPosA != dPosS || codeA != codeS || !bytes.Equal(inA[:wA], inS[:wS]) {
				t.Fatalf("trial %d stringDecode(mode=%d) mismatch: asm=(%d,%d,%d,%d,%d) scalar=(%d,%d,%d,%d,%d)",
					trial, mode, dEndA, wA, sEscA, dPosA, codeA, dEndS, wS, sEscS, dPosS, codeS)
			}
		}

		for _, limit := range []int{0, 2, n + 1} {
			posBufA := make([]int, limit)
			posBufS := make([]int, limit)
			nxA, cntA, fullA := utf8Scan(ptr, n, 5, rt.SliceData(posBufA), limit)
			nxS, cntS, fullS := utf8ScanScalar(ptr, n, 5, rt.SliceData(posBufS), limit)
			if nxA != nxS || cntA != cntS || fullA != fullS {
				t.Fatalf("trial %d utf8Scan(limit=%d) mismatch: asm=(%d,%d,%v) scalar=(%d,%d,%v)",
					trial, limit, nxA, cntA, fullA, nxS, cntS, fullS)
			}
			for k := 0; k < cntA; k++ {
				if posBufA[k] != posBufS[k] {
					t.Fatalf("trial %d utf8Scan pos[%d] mismatch: asm=%d scalar=%d", trial, k, posBufA[k], posBufS[k])
				}
			}
		}

		for _, outCap := range []int{0, n / 2, n, 7*n + 4} {
			dstA := make([]byte, outCap)
			dstS := make([]byte, outCap)
			for _, qflags := range []uint64{0, 1} {
				cA, wA := quote(ptr, n, rt.SliceData(dstA), outCap, qflags)
				cS, wS := quoteScalar(ptr, n, rt.SliceData(dstS), outCap, qflags)
				if cA != cS || wA != wS || !bytes.Equal(dstA[:wA], dstS[:wS]) {
					t.Fatalf("trial %d quote(cap=%d,flags=%d) mismatch: asm=(%d,%d) scalar=(%d,%d)", trial, outCap, qflags, cA, wA, cS, wS)
				}
			}
			cA, wA := quoteHTML(ptr, n, rt.SliceData(dstA), outCap)
			cS, wS := quoteHTMLScalar(ptr, n, rt.SliceData(dstS), outCap)
			if cA != cS || wA != wS || !bytes.Equal(dstA[:wA], dstS[:wS]) {
				t.Fatalf("trial %d quoteHTML(cap=%d) mismatch: asm=(%d,%d) scalar=(%d,%d)", trial, outCap, cA, wA, cS, wS)
			}
			cA, wA = htmlEscape(ptr, n, rt.SliceData(dstA), outCap)
			cS, wS = htmlEscapeScalar(ptr, n, rt.SliceData(dstS), outCap)
			if cA != cS || wA != wS || !bytes.Equal(dstA[:wA], dstS[:wS]) {
				t.Fatalf("trial %d htmlEscape(cap=%d) mismatch: asm=(%d,%d) scalar=(%d,%d)", trial, outCap, cA, wA, cS, wS)
			}
		}
	}
}
