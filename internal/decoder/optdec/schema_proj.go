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
	"reflect"

	"github.com/bytedance/sonic/internal/jsonscan"
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
// It supplies static field-matching and subtree consumption policies (jsonscan.Plan)
// to the shared internal/jsonscan.Machine without owning an independent JSON grammar.
type projPlan struct {
	kind           projKind
	hasRawSubtree  bool
	isBounded      bool
	totalFields    int
	maxStructWidth int
	sd             *structDecoder
	elem           *projPlan
	fields         []*projPlan
	jsPlan         *jsonscan.Plan
	idxPlan        *idxRootPlan
}

var (
	jsScalarPlan      = &jsonscan.Plan{Kind: jsonscan.PlanScalar}
	jsTypedScalarPlan = &jsonscan.Plan{Kind: jsonscan.PlanScalar, TypedNumber: true}
	jsBytesPlan       = &jsonscan.Plan{Kind: jsonscan.PlanBytes, Elem: jsTypedScalarPlan}
	jsRawPlan         = &jsonscan.Plan{Kind: jsonscan.PlanRaw}

	projFullPlan        = &projPlan{kind: projFull, isBounded: false}
	projScalarPlan      = &projPlan{kind: projScalar, isBounded: true, totalFields: 1, jsPlan: jsScalarPlan}
	projTypedScalarPlan = &projPlan{kind: projScalar, isBounded: true, totalFields: 1, jsPlan: jsTypedScalarPlan}
	projBytesPlan       = &projPlan{kind: projBytes, isBounded: true, totalFields: 1, elem: projTypedScalarPlan, jsPlan: jsBytesPlan}
	projRawPlan         = &projPlan{kind: projRaw, hasRawSubtree: true, isBounded: true, totalFields: 1, jsPlan: jsRawPlan}
)

func isSharedProjSingleton(p *projPlan) bool {
	return p == projFullPlan || p == projScalarPlan || p == projTypedScalarPlan || p == projBytesPlan || p == projRawPlan
}

// attachIdxRootPlan attaches idxPlan to plan without mutating shared
// package-init singletons (projFullPlan, projScalarPlan, projBytesPlan, projRawPlan).
// When plan is a shared singleton and idxPlan!= nil, it returns a private shallow copy.
func attachIdxRootPlan(plan *projPlan, idxPlan *idxRootPlan) *projPlan {
	if isSharedProjSingleton(plan) {
		if idxPlan == nil {
			return plan
		}
		cloned := *plan
		cloned.idxPlan = idxPlan
		return &cloned
	}
	plan.idxPlan = idxPlan
	return plan
}

func compileProjPlan(dec decFunc) *projPlan {
	if dec == nil {
		return nil
	}
	idxPlan := compileIdxRootPlan(dec)
	visited := make(map[*structDecoder]*projPlan, 8)
	plan := buildProjPlan(dec, visited, 0)
	if plan == nil || plan.kind == projFull {
		if idxPlan != nil {
			return &projPlan{kind: projFull, idxPlan: idxPlan}
		}
		return nil
	}
	if isSharedProjSingleton(plan) {
		return attachIdxRootPlan(plan, idxPlan)
	}
	attachJSONScanPlan(plan, make(map[*projPlan]*jsonscan.Plan, 8))
	return attachIdxRootPlan(plan, idxPlan)
}

func attachJSONScanPlan(p *projPlan, visited map[*projPlan]*jsonscan.Plan) *jsonscan.Plan {
	if p == nil || p.kind == projFull {
		return nil
	}
	if isSharedProjSingleton(p) || p.jsPlan != nil {
		return p.jsPlan
	}
	if existing, ok := visited[p]; ok {
		return existing
	}
	var jp *jsonscan.Plan
	switch p.kind {
	case projScalar:
		jp = jsScalarPlan
	case projBytes:
		jp = jsBytesPlan
	case projRaw:
		jp = jsRawPlan
	case projStruct:
		fnames := make([]string, len(p.fields))
		jp = &jsonscan.Plan{
			Kind:       jsonscan.PlanStruct,
			Struct:     p.sd,
			FieldNames: fnames,
			Fields:     make([]*jsonscan.Plan, len(p.fields)),
		}
		visited[p] = jp
		for i, fp := range p.fields {
			fnames[i] = p.sd.FieldName(i)
			jp.Fields[i] = attachJSONScanPlan(fp, visited)
		}
	case projSlice:
		jp = &jsonscan.Plan{Kind: jsonscan.PlanSlice}
		visited[p] = jp
		jp.Elem = attachJSONScanPlan(p.elem, visited)
	case projMap:
		jp = &jsonscan.Plan{Kind: jsonscan.PlanMap}
		visited[p] = jp
		jp.Elem = attachJSONScanPlan(p.elem, visited)
	default:
		return nil
	}
	p.jsPlan = jp
	visited[p] = jp
	return jp
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
		*stringDecoder:
		return projTypedScalarPlan
	case *numberDecoder,
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
			elem:        projTypedScalarPlan,
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
			elem:        projTypedScalarPlan,
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
//   - schemaMinJSONBytes (256 B): tiny payloads are cheaper to emit in one linear
//     LexBatch + stepPlainFullEmit pass than initializing schema projection state.
//   - schemaBoundedBytesPerField (24 B/field): struct roots pay off once the JSON
//     exceeds roughly one compact key-value pair per declared field so unknown
//     fields exist to skip without paying duplicate MatchField lookups on dense objects.
//   - schemaWideStructFieldThreshold (40 fields) & schemaWideStructBytesPerField
//     (512 B/field): linear SIMD block-mask scanning processes dense wide objects
//     without per-key FieldMatcher calls during scanning, so strict unknown-field
//     validation pays off when skipped payload dominates declared fields (unless
//     NoValidateJSONSkip enables O(1) container skipping).
//   - schemaUnboundedContainerMaxFields (2 fields): top-level slices/maps of narrow
//     projected structs (<= 2 declared fields) retain ValidateOnly/RelaxedSkip node
//     savings across all payload sizes (including > 4 MiB), while multi-field dense
//     struct slices batch-emit directly via stepPlainFullEmit.
const (
	schemaMinJSONBytes                = 256
	schemaBoundedBytesPerField        = 24
	schemaWideStructFieldThreshold    = 40
	schemaWideStructBytesPerField     = 512
	schemaUnboundedMinJSONBytes       = 384
	schemaUnboundedMaxStructWidth     = 32
	schemaUnboundedMaxTotalFields     = 64
	schemaUnboundedContainerMaxFields = 2
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
	if p.kind == projSlice || p.kind == projMap {
		return jsonLen >= schemaUnboundedMinJSONBytes &&
			p.maxStructWidth > 0 &&
			p.maxStructWidth <= schemaUnboundedContainerMaxFields &&
			p.totalFields <= schemaUnboundedContainerMaxFields
	}
	return jsonLen >= schemaUnboundedMinJSONBytes &&
		jsonLen >= p.totalFields*schemaBoundedBytesPerField &&
		p.maxStructWidth > 0 &&
		p.maxStructWidth <= schemaUnboundedMaxStructWidth &&
		p.totalFields <= schemaUnboundedMaxTotalFields
}
