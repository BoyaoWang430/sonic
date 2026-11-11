package optdec

import (
	"reflect"
	"sync/atomic"
)

type projKind uint8

const (
	projFull projKind = iota
	projScalar
	projRaw
	projStruct
	projSlice
	projMap
)

// projPlan is an immutable compile-time schema summary attached to root decoders.
// It enables Schema-Aware Sparse Tape scanning to validate-and-skip unknown
// object subtrees and RawMessage/Unmarshaler payloads in place without emitting
// child Tape nodes or converting unused floating-point numbers.
type projPlan struct {
	kind           projKind
	hasRawSubtree  bool
	isBounded      bool
	allASCIIKeys   bool
	hasLongKey     bool
	totalFields    int
	maxStructWidth int
	keyLenMask     uint64
	firstLo        uint64
	firstHi        uint64
	sd             *structDecoder
	elem           *projPlan
	fields         []*projPlan
	denseStreak    uint32
	probeTick      uint32
}

func (p *projPlan) recordDense() {
	if atomic.LoadUint32(&p.denseStreak) < 16 {
		atomic.AddUint32(&p.denseStreak, 1)
	}
}

func (p *projPlan) recordSparse() {
	if atomic.LoadUint32(&p.denseStreak) != 0 {
		atomic.StoreUint32(&p.denseStreak, 0)
	}
}

var (
	projFullPlan   = &projPlan{kind: projFull, isBounded: false}
	projScalarPlan = &projPlan{kind: projScalar, isBounded: true, totalFields: 1}
	projRawPlan    = &projPlan{kind: projRaw, hasRawSubtree: true, isBounded: true, totalFields: 1}
)

func compileProjPlan(dec decFunc) *projPlan {
	if dec == nil {
		return nil
	}
	visited := make(map[*structDecoder]*projPlan, 8)
	plan := buildProjPlan(dec, visited, 0)
	if plan == nil || plan.kind == projFull {
		return nil
	}
	return plan
}

func addKeyFirstByteBit(lo, hi *uint64, ch byte) {
	if ch < 64 {
		*lo |= uint64(1) << uint(ch)
	} else if ch < 128 {
		*hi |= uint64(1) << uint(ch-64)
	}
}

func buildProjPlan(dec decFunc, visited map[*structDecoder]*projPlan, depth int) *projPlan {
	if dec == nil || depth > 32 {
		return projFullPlan
	}
	switch d := dec.(type) {
	case *rawMessageDecoder:
		return projRawPlan
	case *noCopyRawMessageDecoder:
		if d.strOpt {
			return projScalarPlan
		}
		return projRawPlan
	case *unmarshalJSONDecoder:
		if d.strOpt {
			return projScalarPlan
		}
		if d.typ != nil && d.typ.Kind() == reflect.Interface {
			// Interface Unmarshaler checks IsNull() then AsRaw(); KNull/KRaw satisfy both.
			return projRawPlan
		}
		return projRawPlan
	case *unmarshalTextDecoder:
		return projScalarPlan
	case *boolDecoder,
		*i8Decoder, *i16Decoder, *i32Decoder, *i64Decoder,
		*u8Decoder, *u16Decoder, *u32Decoder, *u64Decoder,
		*f32Decoder, *f64Decoder,
		*stringDecoder, *numberDecoder,
		*strStringDecoder, *numberStringDecoder, *boolStringDecoder,
		*i8StringDecoder, *i16StringDecoder, *i32StringDecoder, *i64StringDecoder,
		*u8StringDecoder, *u16StringDecoder, *u32StringDecoder, *u64StringDecoder,
		*f32StringDecoder, *f64StringDecoder, *ptrStrDecoder:
		return projScalarPlan
	case *ptrDecoder:
		return buildProjPlan(d.deref, visited, depth+1)
	case *embeddedFieldPtrDecoder:
		return buildProjPlan(d.fieldDec, visited, depth+1)
	case *structDecoder:
		if existing, ok := visited[d]; ok {
			if existing == nil {
				// Recursive struct cycle: fall back to full subtree scan at cycle edge.
				return projFullPlan
			}
			return existing
		}
		visited[d] = nil
		n := len(d.fields)
		fplans := make([]*projPlan, n)
		hasRaw := false
		bounded := true
		allASCII := true
		hasLong := false
		var lenMask, fLo, fHi uint64
		total := n
		maxW := n
		for i := 0; i < n; i++ {
			name := d.fields[i].name
			if len(name) >= 64 {
				hasLong = true
			} else {
				lenMask |= uint64(1) << uint(len(name))
			}
			for j := 0; j < len(name); j++ {
				if name[j] >= 0x80 {
					allASCII = false
					break
				}
			}
			if len(name) > 0 && name[0] < 0x80 {
				b := name[0]
				addKeyFirstByteBit(&fLo, &fHi, b)
				if b >= 'A' && b <= 'Z' {
					addKeyFirstByteBit(&fLo, &fHi, b|0x20)
				} else if b >= 'a' && b <= 'z' {
					addKeyFirstByteBit(&fLo, &fHi, b&^0x20)
				}
			}

			fp := buildProjPlan(d.fieldDecs[i], visited, depth+1)
			fplans[i] = fp
			if fp.hasRawSubtree {
				hasRaw = true
			}
			if !fp.isBounded {
				bounded = false
			}
			if fp.kind == projStruct || fp.kind == projSlice || fp.kind == projMap {
				total += fp.totalFields
			}
			if fp.maxStructWidth > maxW {
				maxW = fp.maxStructWidth
			}
		}
		p := &projPlan{
			kind:           projStruct,
			hasRawSubtree:  hasRaw,
			isBounded:      bounded,
			allASCIIKeys:   allASCII,
			hasLongKey:     hasLong,
			totalFields:    total,
			maxStructWidth: maxW,
			keyLenMask:     lenMask,
			firstLo:        fLo,
			firstHi:        fHi,
			sd:             d,
			fields:         fplans,
		}
		visited[d] = p
		return p
	case *sliceDecoder:
		ep := buildProjPlan(d.elemDec, visited, depth+1)
		if ep.kind == projFull && !ep.hasRawSubtree {
			return projFullPlan
		}
		return &projPlan{
			kind:           projSlice,
			hasRawSubtree:  ep.hasRawSubtree,
			isBounded:      false,
			totalFields:    ep.totalFields,
			maxStructWidth: ep.maxStructWidth,
			elem:           ep,
		}
	case *sliceI64Decoder, *sliceI32Decoder, *sliceU64Decoder, *sliceU32Decoder,
		*sliceF64Decoder, *sliceF32Decoder, *sliceBoolDecoder, *sliceStringDecoder:
		return &projPlan{
			kind:        projSlice,
			isBounded:   false,
			totalFields: 1,
			elem:        projScalarPlan,
		}
	case *arrayDecoder:
		ep := buildProjPlan(d.elemDec, visited, depth+1)
		if ep.kind == projFull && !ep.hasRawSubtree {
			return projFullPlan
		}
		tot := d.len
		if ep.totalFields > 1 {
			tot = d.len * ep.totalFields
		}
		return &projPlan{
			kind:           projSlice,
			hasRawSubtree:  ep.hasRawSubtree,
			isBounded:      ep.isBounded,
			totalFields:    tot,
			maxStructWidth: ep.maxStructWidth,
			elem:           ep,
		}
	case *mapStrKeyDecoder:
		ep := buildProjPlan(d.elemDec, visited, depth+1)
		if ep.kind != projStruct && ep.kind != projRaw && !ep.hasRawSubtree {
			return projFullPlan
		}
		return &projPlan{
			kind:           projMap,
			hasRawSubtree:  ep.hasRawSubtree,
			isBounded:      false,
			totalFields:    ep.totalFields,
			maxStructWidth: ep.maxStructWidth,
			elem:           ep,
		}
	default:
		return projFullPlan
	}
}

func (p *projPlan) shouldUse(jsonLen int) bool {
	if p == nil || p.kind == projFull || p.kind == projScalar {
		return false
	}
	if p.hasRawSubtree {
		return jsonLen >= 64
	}
	if jsonLen < 256 {
		return false
	}
	if atomic.LoadUint32(&p.denseStreak) >= 2 {
		if (atomic.AddUint32(&p.probeTick, 1) & 255) != 0 {
			return false
		}
	}
	if p.isBounded {
		return jsonLen >= p.totalFields*48
	}
	return jsonLen >= 512 && p.maxStructWidth > 0 && p.maxStructWidth <= 12 && p.totalFields <= 24
}
