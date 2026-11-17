//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"math/bits"
	"reflect"
	"unsafe"

	"github.com/bytedance/sonic/internal/rt"
)

// ScanContext holds only scanning-stage state: the compact ScanKernelState ABI,
// input padding/unescape scratch buffers, and scan control flags. It contains
// no target allocators or materialization state.
type ScanContext struct {
	ScanKernelState
	padded             []byte
	paddedBackup       []byte
	sessionPadded      []byte
	unescBuf           []byte
	dbuf               []byte
	backup             []node
	options            uint64
	isEface            bool
	strictURC          bool
	keepUseNumber      bool
	lazyPadded         bool
	singleDoc          bool
	deferFloatOverflow bool
	tailBuf            [320]byte
}

// Document holds the immutable-view JSON source, node tape, structural stats,
// and decoded escaped-string storage produced by a scan pass and read by
// materialization (Node.AsXxx and type decoders).
type Document struct {
	Json         string
	inPlaceBytes []byte
	unesc        []byte
	nodes        []node
	stat         jsonStat
	hasProjHints bool
	Utf8Inv      bool
}

func (d *Document) Root() Node {
	return Node{ptr: unsafe.SliceData(d.nodes)}
}

// rawSlice returns a zero-copy substring of the source JSON document [start:end].
func (d *Document) rawSlice(start, end int) string {
	return d.Json[start:end]
}

// strRefCommon returns a zero-copy string view of an unescaped JSON string token.
func (d *Document) strRefCommon(off, length int) string {
	return d.Json[off : off+length]
}

// bytesEscaped returns the decoded byte slice for an escaped JSON string token.
func (d *Document) bytesEscaped(off int, val uint64) []byte {
	if val&escapedInUnescFlag != 0 {
		u := val &^ escapedInUnescFlag
		uoff := int(u >> 32)
		ulen := int(uint32(u))
		return d.unesc[uoff : uoff+ulen]
	}
	return d.inPlaceBytes[off : off+int(val)]
}

// strRefEscaped returns a transient string view of a decoded escaped JSON string token.
func (d *Document) strRefEscaped(off int, val uint64) string {
	return rt.Mem2Str(d.bytesEscaped(off, val))
}

// DecodeContext (aliased as Context across materialization decoders) owns
// materialization options, the read-only Document view, and target-value
// arenas. Backing storage referenced by returned Go values is detached on
// resetMaterialization and never returned to any pool.
type DecodeContext = Context

type Context struct {
	Doc            Document
	options        uint64
	isEface        bool
	Utf8Inv        bool
	ownedParser    *Parser
	sliceArena     []interface{}
	sliceIdx       int
	strSlab        []byte
	strSlabOff     int
	strSlabNext    int
	scalarSlab     []uint64
	scalarSlabOff  int
	scalarSlabNext int
	strPtrSlab     []string
	strPtrSlabOff  int
	strPtrSlabNext int
	ptrStructSlabs [4]ptrStructSlabSlot
	strAnyMask     uint32
	strAnyCache    [32]strAnySlot
}

func (ctx *Context) Options() uint64 {
	return ctx.options
}

func (ctx *Context) Root() Node {
	return ctx.Doc.Root()
}

func (ctx *Context) initForDocument(doc Document, opts uint64, root reflect.Type) {
	ctx.resetMaterialization()
	ctx.Doc = doc
	ctx.options = opts
	ctx.isEface = root == anyType || root == mapEfaceType || root == sliceEfaceType
	ctx.Utf8Inv = doc.Utf8Inv
	if canUseFastMap(opts, root) {
		ctx.initSliceArena()
	}
}

// resetMaterialization detaches all references to user-owned allocations before
// scan scratch buffers are recycled. None of strSlab, scalarSlab, strPtrSlab,
// sliceArena, or ptrStructSlabs are returned to a pool.
func (ctx *Context) resetMaterialization() {
	ctx.Doc = Document{}
	ctx.options = 0
	ctx.isEface = false
	ctx.Utf8Inv = false
	ctx.sliceArena = nil
	ctx.sliceIdx = 0
	ctx.strSlab = nil
	ctx.strSlabOff = 0
	ctx.strSlabNext = 0
	ctx.scalarSlab = nil
	ctx.scalarSlabOff = 0
	ctx.scalarSlabNext = 0
	ctx.strPtrSlab = nil
	ctx.strPtrSlabOff = 0
	ctx.strPtrSlabNext = 0
	if ctx.ptrStructSlabs[0].sd != nil {
		ctx.ptrStructSlabs = [4]ptrStructSlabSlot{}
	}
	if ctx.strAnyMask != 0 {
		ctx.clearStrAnyCache()
	}
}

func (ctx *Context) Delete() {
	p := ctx.ownedParser
	ctx.ownedParser = nil
	ctx.resetMaterialization()
	if p != nil {
		p.free()
	}
}

func (ctx *Context) initSliceArena() {
	if n := int(ctx.Doc.stat.array_elems); n > 0 && ctx.sliceArena == nil {
		ctx.sliceArena = make([]interface{}, n)
		ctx.sliceIdx = 0
	}
}

func (ctx *Context) allocEfaceSlice(size int) []interface{} {
	if size == 0 {
		return []interface{}{}
	}
	if rem := len(ctx.sliceArena) - ctx.sliceIdx; rem >= size {
		s := ctx.sliceArena[ctx.sliceIdx : ctx.sliceIdx+size : ctx.sliceIdx+size]
		ctx.sliceIdx += size
		return s
	}
	return make([]interface{}, size)
}

func (ctx *Context) clearStrAnyCache() {
	for m := ctx.strAnyMask; m != 0; m &= m - 1 {
		ctx.strAnyCache[bits.TrailingZeros32(m)] = strAnySlot{}
	}
	ctx.strAnyMask = 0
}

func (ctx *Context) allocScalarPtr(hint int) unsafe.Pointer {
	off := ctx.scalarSlabOff
	if off < len(ctx.scalarSlab) {
		ctx.scalarSlabOff = off + 1
		return unsafe.Pointer(&ctx.scalarSlab[off])
	}
	return ctx.allocScalarPtrSlow(hint)
}

//go:noinline
func (ctx *Context) allocScalarPtrSlow(hint int) unsafe.Pointer {
	n := ctx.scalarSlabNext
	if n == 0 {
		if hint >= 1 && hint <= 16 {
			n = hint
		} else if hint > 16 {
			n = 16
		} else {
			n = 4
		}
	}
	next := n * 4
	if next < 8 {
		next = 8
	} else if next > maxPtrSlabCap {
		next = maxPtrSlabCap
	}
	ctx.scalarSlabNext = next
	slab := make([]uint64, n)
	ctx.scalarSlab = slab
	ctx.scalarSlabOff = 1
	return unsafe.Pointer(&slab[0])
}

func (ctx *Context) allocStrPtr(hint int) *string {
	off := ctx.strPtrSlabOff
	if off < len(ctx.strPtrSlab) {
		ctx.strPtrSlabOff = off + 1
		return &ctx.strPtrSlab[off]
	}
	return ctx.allocStrPtrSlow(hint)
}

//go:noinline
func (ctx *Context) allocStrPtrSlow(hint int) *string {
	n := ctx.strPtrSlabNext
	if n == 0 {
		if hint >= 1 && hint <= 16 {
			n = hint
		} else if hint > 16 {
			n = 16
		} else {
			n = 4
		}
	}
	next := n * 4
	if next < 8 {
		next = 8
	} else if next > maxPtrSlabCap {
		next = maxPtrSlabCap
	}
	ctx.strPtrSlabNext = next
	slab := make([]string, n)
	ctx.strPtrSlab = slab
	ctx.strPtrSlabOff = 1
	return &slab[0]
}

// strCommon returns an unescaped JSON string token, copying if copyStr is true.
func (ctx *Context) strCommon(off, length int, copyStr bool) string {
	s := ctx.Doc.Json[off : off+length]
	if !copyStr {
		return s
	}
	return ctx.copyArenaString(s)
}

func (ctx *Context) copyArenaHit(s string) (string, bool) {
	n := len(s)
	off := ctx.strSlabOff
	if len(ctx.strSlab)-off >= n {
		ctx.strSlabOff = off + n
		dst := ctx.strSlab[off : off+n]
		copy(dst, s)
		return unsafe.String(&dst[0], n), true
	}
	return s, false
}

func (ctx *Context) copyArenaString(s string) string {
	if len(s) == 0 {
		return ""
	}
	if out, ok := ctx.copyArenaHit(s); ok {
		return out
	}
	return ctx.copyArenaStringSlow(s)
}

//go:noinline
func (ctx *Context) copyArenaStringSlow(s string) string {
	n := len(s)
	if n > maxArenaStrLen {
		return string(rt.Str2Mem(s))
	}
	slab := ctx.allocStrSlab(n)
	copy(slab[:n], s)
	return unsafe.String(&slab[0], n)
}

//go:noinline
func (ctx *Context) copyArenaBytesSlow(b []byte) string {
	n := len(b)
	if n > maxArenaStrLen {
		return string(b)
	}
	slab := ctx.allocStrSlab(n)
	copy(slab[:n], b)
	return unsafe.String(&slab[0], n)
}

func (ctx *Context) allocStrSlab(n int) []byte {
	jsonLen := len(ctx.Doc.Json)
	slabMax := maxStrSlabCap
	if jsonLen <= 32768 {
		slabMax = 4096
	}
	nextCap := ctx.strSlabNext
	if nextCap == 0 {
		if ctx.options&(1<<_F_copy_string) == 0 {
			if jsonLen > 1024 {
				nextCap = 512
			} else {
				nextCap = 128
			}
		} else if ctx.isEface {
			nextCap = jsonLen / 2
			if nextCap < minStrSlabCap {
				nextCap = minStrSlabCap
			} else if nextCap > slabMax {
				nextCap = slabMax
			}
		} else if jsonLen <= 128 {
			nextCap = minStrSlabCap
		} else if jsonLen <= 1024 {
			nextCap = 256
		} else {
			nextCap = jsonLen / 4
			if nextCap < initStrSlabCap {
				nextCap = initStrSlabCap
			} else if nextCap > slabMax {
				nextCap = slabMax
			}
		}
	}
	if nextCap < n*2 {
		nextCap = n * 2
	}
	growCap := nextCap * 2
	if growCap > slabMax {
		growCap = slabMax
	}
	ctx.strSlabNext = growCap
	slab := make([]byte, nextCap)
	ctx.strSlab = slab
	ctx.strSlabOff = n
	return slab
}

// strEscaped returns a heap-copied string of a decoded escaped JSON string token.
func (ctx *Context) strEscaped(off int, val uint64) string {
	return ctx.strEscapedBytes(ctx.Doc.bytesEscaped(off, val))
}

func (ctx *Context) strEscapedBytes(b []byte) string {
	n := len(b)
	if n == 0 {
		return ""
	}
	slabOff := ctx.strSlabOff
	if len(ctx.strSlab)-slabOff >= n {
		ctx.strSlabOff = slabOff + n
		dst := ctx.strSlab[slabOff : slabOff+n]
		copy(dst, b)
		return unsafe.String(&dst[0], n)
	}
	return ctx.copyArenaBytesSlow(b)
}
