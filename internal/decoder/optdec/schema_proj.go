//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

import (
	"reflect"
)

type projKind uint8

const (
	projFull projKind = iota
	projScalar
	projBytes
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
	totalFields    int
	maxStructWidth int
	sd             *structDecoder
	elem           *projPlan
	fields         []*projPlan
}

var (
	projFullPlan        = &projPlan{kind: projFull, isBounded: false}
	projScalarPlan      = &projPlan{kind: projScalar, isBounded: true, totalFields: 1}
	projBytesPlan       = &projPlan{kind: projBytes, isBounded: true, totalFields: 1, elem: projScalarPlan}
	projRawPlan         = &projPlan{kind: projRaw, hasRawSubtree: true, isBounded: true, totalFields: 1}
	schemaFallbackCount uint64
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
		total := n
		maxW := n
		for i := 0; i < n; i++ {
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
			totalFields:    total,
			maxStructWidth: maxW,
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
	case *sliceBytesDecoder:
		return projBytesPlan
	case *sliceIntDecoder, *sliceI64Decoder, *sliceI32Decoder,
		*sliceUintDecoder, *sliceU64Decoder, *sliceU32Decoder,
		*sliceF64Decoder, *sliceF32Decoder, *sliceBoolDecoder, *sliceStringDecoder:
		return &projPlan{
			kind:        projSlice,
			isBounded:   false,
			totalFields: 1,
			elem:        projScalarPlan,
		}
	case *mapStringDecoder, *mapStrIntDecoder, *mapStrI64Decoder,
		*mapStrSignedDecoder[int8], *mapStrSignedDecoder[int16], *mapStrSignedDecoder[int32],
		*mapStrUnsignedDecoder[uint], *mapStrUnsignedDecoder[uint8], *mapStrUnsignedDecoder[uint16],
		*mapStrUnsignedDecoder[uint32], *mapStrU64Decoder, *mapStrUnsignedDecoder[uintptr],
		*mapStrF32Decoder, *mapStrF64Decoder, *mapStrBoolDecoder:
		return &projPlan{
			kind:        projMap,
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
	return p.shouldUseOpts(jsonLen, 0)
}

// Deterministic, stateless schema-projection admission thresholds:
//   - schemaMinJSONBytes (256 B): tiny payloads are cheaper to scan in one linear
//     Runs pass than initializing projection state.
//   - schemaBoundedBytesPerField (24 B/field): bounded structs pay off once the
//     JSON exceeds roughly one compact key-value pair per declared field.
//   - schemaWideStructFieldThreshold (40 fields) & schemaWideStructBytesPerField
//     (512 B/field): linear SIMD Runs scanning processes dense wide objects rapidly
//     in a single pass, so per-key schema dispatch and strict unknown-field
//     validation pay off when skipped payload dominates declared fields (unless
//     NoValidateJSONSkip enables O(1) container skipping).
const (
	schemaMinJSONBytes             = 256
	schemaBoundedBytesPerField     = 24
	schemaWideStructFieldThreshold = 40
	schemaWideStructBytesPerField  = 512
	schemaUnboundedMinJSONBytes    = 384
	schemaUnboundedMaxStructWidth  = 32
	schemaUnboundedMaxTotalFields  = 64
)

func (p *projPlan) shouldUseOpts(jsonLen int, f uint64) bool {
	if p == nil || p.kind == projFull || p.kind == projScalar || p.kind == projBytes {
		return false
	}
	if p.kind == projRaw || p.hasRawSubtree {
		return jsonLen > 0
	}
	if jsonLen < schemaMinJSONBytes {
		return false
	}
	if p.isBounded {
		if f&(1<<_F_no_validate_json) == 0 && p.maxStructWidth >= schemaWideStructFieldThreshold {
			return jsonLen >= p.totalFields*schemaWideStructBytesPerField
		}
		return jsonLen >= p.totalFields*schemaBoundedBytesPerField
	}
	return jsonLen >= schemaUnboundedMinJSONBytes &&
		p.maxStructWidth > 0 &&
		p.maxStructWidth <= schemaUnboundedMaxStructWidth &&
		p.totalFields <= schemaUnboundedMaxTotalFields
}
