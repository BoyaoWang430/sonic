//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import "unsafe"

const (
	paddedSentinel   byte = 'x'
	paddedBlockWidth int  = 64
)

// paddedInputWindow is the single auditable adapter for fixed-width block
// helpers (cParityNonspace64, cParityStringMasks32, cParityNumberMasks16).
// Callers pass logical byte positions without knowing underlying AVX2/NEON/RVV
// read widths or managing tailPtr / safe64 / tailStart coordinates.
type paddedInputWindow struct {
	strPtr    unsafe.Pointer
	tailPtr   unsafe.Pointer
	end       int
	safeLimit int
	tailStart int
}

func newPaddedWindowLazy(jsonStr string, tailScratch *[320]byte) paddedInputWindow {
	end := len(jsonStr)
	if end <= schemaTailWindowLen {
		copy(tailScratch[:end], jsonStr)
		copy(tailScratch[end:end+len(padding)], padding)
		p := unsafe.Pointer(&tailScratch[0])
		return paddedInputWindow{
			strPtr:    p,
			tailPtr:   p,
			end:       end,
			safeLimit: end,
		}
	}
	tailStart := end - schemaTailWindowLen
	copy(tailScratch[:schemaTailWindowLen], jsonStr[tailStart:end])
	copy(tailScratch[schemaTailWindowLen:schemaTailWindowLen+len(padding)], padding)
	return paddedInputWindow{
		strPtr:    unsafe.Pointer(unsafe.StringData(jsonStr)),
		tailPtr:   unsafe.Pointer(&tailScratch[0]),
		end:       end,
		safeLimit: end - paddedBlockWidth,
		tailStart: tailStart,
	}
}

func newPaddedWindowPadded(raw []byte, end int) (paddedInputWindow, bool) {
	if len(raw) < end+len(padding) {
		return paddedInputWindow{}, false
	}
	p := unsafe.Pointer(unsafe.SliceData(raw))
	return paddedInputWindow{
		strPtr:    p,
		tailPtr:   p,
		end:       end,
		safeLimit: end,
	}, true
}

func (w paddedInputWindow) byteAt(pos int) byte {
	if pos < w.end {
		return *(*byte)(unsafe.Add(w.strPtr, pos))
	}
	return paddedSentinel
}

func (w paddedInputWindow) blockPtr(pos int) unsafe.Pointer {
	if pos <= w.safeLimit {
		return unsafe.Add(w.strPtr, pos)
	}
	return unsafe.Add(w.tailPtr, pos-w.tailStart)
}

func (w paddedInputWindow) nonspace64(pos int) uint64 {
	return cParityNonspace64(w.blockPtr(pos))
}

func (w paddedInputWindow) stringMasks32(pos int, opts uint64) (bs, quote, control uint32) {
	return cParityStringMasks32(w.blockPtr(pos), opts)
}

func (w paddedInputWindow) numberMasks16(pos int) (decimal, exponent, sign, valid uint32) {
	return cParityNumberMasks16(w.blockPtr(pos))
}

// terminatedNumberPtr returns a contiguous padded pointer for cParityParseNumberValue
// starting at numBody when the number token does not cross from unpadded main input
// into the tail window. When exactEnd >= 0, the caller verifies nextCur consumed up
// to exactEnd; when ok is false, the token straddles the tail boundary.
func (w paddedInputWindow) terminatedNumberPtr(numBody, scanFrom int) (startPtr unsafe.Pointer, exactEnd int, ok bool) {
	if w.strPtr == w.tailPtr {
		return unsafe.Add(w.strPtr, numBody), -1, true
	}
	if numBody >= w.tailStart {
		return unsafe.Add(w.tailPtr, numBody-w.tailStart), -1, true
	}
	endCur := scanFrom
	for endCur <= w.safeLimit && isNumberContByte(*(*byte)(unsafe.Add(w.strPtr, endCur))) {
		endCur++
	}
	if endCur <= w.safeLimit {
		return unsafe.Add(w.strPtr, numBody), endCur, true
	}
	return nil, 0, false
}
