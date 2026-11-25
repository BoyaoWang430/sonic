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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"
	"unsafe"

	"github.com/bytedance/sonic/internal/ident"
	"github.com/bytedance/sonic/internal/jsonscan"
)

// ============================================================================
// R6 Target Matrix (§9.3: I1–I6 Index-Admitted, T1 Tape-Excluded, S1/S2 Stream)
// ============================================================================

// I1: Ordinary struct without array/map/method exclusions -> static idxPlan != nil.
type r6IdxStruct struct {
	ID      int64       `json:"id"`
	U64     uint64      `json:"u64"`
	I32     int32       `json:"i32"`
	F32     float32     `json:"f32"`
	F64     float64     `json:"f64"`
	Flag    bool        `json:"flag"`
	Name    string      `json:"name"`
	PtrInt  *int64      `json:"ptr_int"`
	PtrInt2 *int64      `json:"ptr_int2"`
	PtrStr  *string     `json:"ptr_str"`
	Nums    []int       `json:"nums"`
	Nums2   []int       `json:"nums2"`
	F32s    []float32   `json:"f32s"`
	Tags    []string    `json:"tags"`
	Kids    []diffChild `json:"kids"`
	Child   diffChild   `json:"child"`
}

// I4 / I5 / I6: Static index-admitted root struct with dynamic any / []any fields.
type r6IdxDynamicStruct struct {
	ID      int64   `json:"id"`
	F32     float32 `json:"f32"`
	F64     float64 `json:"f64"`
	Name    string  `json:"name"`
	Sibling int     `json:"sibling"`
	Any     any     `json:"any"`
	Anys    []any   `json:"anys"`
	Nums    []int   `json:"nums"`
}

type r6DynCallbackProbe struct {
	V          int       `json:"v"`
	MutSibling *int      `json:"-"`
	Log        *[]string `json:"-"`
}

func (p *r6DynCallbackProbe) UnmarshalJSON(data []byte) error {
	if p.Log != nil {
		*p.Log = append(*p.Log, string(data))
	}
	if p.MutSibling != nil {
		*p.MutSibling += 100
	}
	type plain struct {
		V int `json:"v"`
	}
	var pl plain
	if err := json.Unmarshal(data, &pl); err != nil {
		return err
	}
	p.V = pl.V
	return nil
}

var (
	r6FuzzExecs               atomic.Uint64
	r6FuzzIndexScans          atomic.Uint64
	r6FuzzIndexBinds          atomic.Uint64
	r6FuzzSubtreeHits         atomic.Uint64
	r6FuzzSyntaxBlocks        atomic.Uint64
	r6FuzzNoValInvalidSkipped atomic.Uint64
	r6FuzzProducerHits        [16]atomic.Uint64
	r6FuzzFamilyI1            atomic.Uint64
	r6FuzzFamilyI2            atomic.Uint64
	r6FuzzFamilyI3            atomic.Uint64
	r6FuzzFamilyI4I6          atomic.Uint64
	r6FuzzFamilyNoVal         atomic.Uint64
)

func resetR6FuzzStats() {
	r6FuzzExecs.Store(0)
	r6FuzzIndexScans.Store(0)
	r6FuzzIndexBinds.Store(0)
	r6FuzzSubtreeHits.Store(0)
	r6FuzzSyntaxBlocks.Store(0)
	r6FuzzNoValInvalidSkipped.Store(0)
	for i := range r6FuzzProducerHits {
		r6FuzzProducerHits[i].Store(0)
	}
	r6FuzzFamilyI1.Store(0)
	r6FuzzFamilyI2.Store(0)
	r6FuzzFamilyI3.Store(0)
	r6FuzzFamilyI4I6.Store(0)
	r6FuzzFamilyNoVal.Store(0)
}

func recordR6ObservedProducers(mask jsonscan.LexProducerMask) {
	for p := jsonscan.LexProducer(1); p < 16; p++ {
		if mask.Has(p) {
			r6FuzzProducerHits[p].Add(1)
		}
	}
}

func assertR6RouteProducer(t *testing.T, routeName string, forceScalar bool, w routeWitness) {
	t.Helper()
	if !jsonscan.WitnessEnabled {
		if w.ObservedProducers != 0 || w.PositionProducers != 0 || w.SubtreeProducers != 0 {
			t.Fatalf("N3 default build unexpectedly recorded non-zero producer mask on %s: main=%v pos=%v sub=%v",
				routeName, w.ObservedProducers.Names(), w.PositionProducers.Names(), w.SubtreeProducers.Names())
		}
		return
	}
	wantFamily := expectedDispatchedFamilyFromIdent(forceScalar)
	want := jsonscan.LexProducerBit(wantFamily)
	if got := jsonscan.ExpectedDispatchedLexFamily(forceScalar); got != wantFamily {
		t.Fatalf("N4 dispatched family table drift on %s (forceScalar=%v): table=%v ident=%v",
			routeName, forceScalar, got, wantFamily)
	}
	if w.ObservedProducers != 0 && w.ObservedProducers != want {
		t.Fatalf("F3 family drift on %s (forceScalar=%v): observed=%v want=%v",
			routeName, forceScalar, w.ObservedProducers.Names(), want.Names())
	}
	if w.PositionProducers != 0 && w.PositionProducers != want {
		t.Fatalf("F3 sidecar family drift on %s (forceScalar=%v): observed=%v want=%v",
			routeName, forceScalar, w.PositionProducers.Names(), want.Names())
	}
	if w.SubtreeProducers != 0 && w.SubtreeProducers != want {
		t.Fatalf("F3 subtree family drift on %s (forceScalar=%v): observed=%v want=%v",
			routeName, forceScalar, w.SubtreeProducers.Names(), want.Names())
	}
}

// r6Diff describes a structured state/value/error/topology/witness divergence
// detected by the unified 5-route comparator (F2 / H03).
type r6Diff struct {
	Category string
	Path     string
	Detail   string
}

func (d r6Diff) String() string {
	return fmt.Sprintf("[%s] %s: %s", d.Category, d.Path, d.Detail)
}

type r6BundleAnchors struct {
	extAAddr     uintptr
	extBAddr     uintptr
	backingStart uintptr
	backingEnd   uintptr
	rawSlotAddr  uintptr
	logsAddr     uintptr
	dstStart     uintptr
	dstEnd       uintptr
	srcStart     uintptr
	srcEnd       uintptr
}

type r6TopologyTracker struct {
	anchors r6BundleAnchors
	ptrIDs  map[uintptr]string
	slcIDs  map[uintptr]string
	nextPtr int
	nextSlc int
}

func newR6TopologyTracker(a r6BundleAnchors) *r6TopologyTracker {
	return &r6TopologyTracker{
		anchors: a,
		ptrIDs:  make(map[uintptr]string, 8),
		slcIDs:  make(map[uintptr]string, 8),
	}
}

func (tr *r6TopologyTracker) tagAddr(addr uintptr, isSlice bool) string {
	if addr == 0 {
		return "nil"
	}
	a := tr.anchors
	switch {
	case !isSlice && a.extAAddr != 0 && addr == a.extAAddr:
		return "anchor:extA"
	case !isSlice && a.extBAddr != 0 && addr == a.extBAddr:
		return "anchor:extB"
	case !isSlice && a.rawSlotAddr != 0 && addr == a.rawSlotAddr:
		return "anchor:rawSlot"
	case !isSlice && a.logsAddr != 0 && addr == a.logsAddr:
		return "anchor:logs"
	case a.backingStart != 0 && addr >= a.backingStart && addr < a.backingEnd:
		return fmt.Sprintf("anchor:backing+%d", addr-a.backingStart)
	case a.srcStart != 0 && addr >= a.srcStart && addr < a.srcEnd:
		return fmt.Sprintf("anchor:src+%d", addr-a.srcStart)
	case !isSlice && a.dstStart != 0 && addr >= a.dstStart && addr < a.dstEnd:
		return fmt.Sprintf("anchor:dst+%d", addr-a.dstStart)
	}
	if isSlice {
		if id, ok := tr.slcIDs[addr]; ok {
			return id
		}
		tr.nextSlc++
		id := fmt.Sprintf("slice#%d", tr.nextSlc)
		tr.slcIDs[addr] = id
		return id
	}
	if id, ok := tr.ptrIDs[addr]; ok {
		return id
	}
	tr.nextPtr++
	id := fmt.Sprintf("ptr#%d", tr.nextPtr)
	tr.ptrIDs[addr] = id
	return id
}

type r6PtrPair struct {
	a, b uintptr
}

func compareR6Values(path string, ref, got reflect.Value, refTopo, gotTopo *r6TopologyTracker, seen map[r6PtrPair]bool, out *[]r6Diff) {
	if !ref.IsValid() || !got.IsValid() {
		if ref.IsValid() != got.IsValid() {
			*out = append(*out, r6Diff{"validity", path, fmt.Sprintf("ref=%v got=%v", ref.IsValid(), got.IsValid())})
		}
		return
	}
	if ref.Type() != got.Type() {
		*out = append(*out, r6Diff{"type", path, fmt.Sprintf("ref=%v got=%v", ref.Type(), got.Type())})
		return
	}
	switch ref.Kind() {
	case reflect.Bool:
		if ref.Bool() != got.Bool() {
			*out = append(*out, r6Diff{"bool", path, fmt.Sprintf("ref=%v got=%v", ref.Bool(), got.Bool())})
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if ref.Int() != got.Int() {
			*out = append(*out, r6Diff{"int", path, fmt.Sprintf("ref=%d got=%d", ref.Int(), got.Int())})
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if ref.Uint() != got.Uint() {
			*out = append(*out, r6Diff{"uint", path, fmt.Sprintf("ref=%d got=%d", ref.Uint(), got.Uint())})
		}
	case reflect.Float32:
		ba := math.Float32bits(float32(ref.Float()))
		bb := math.Float32bits(float32(got.Float()))
		if ba != bb {
			*out = append(*out, r6Diff{"float32_bits", path, fmt.Sprintf("ref=%#08x (%g) got=%#08x (%g)", ba, ref.Float(), bb, got.Float())})
		}
	case reflect.Float64:
		ba := math.Float64bits(ref.Float())
		bb := math.Float64bits(got.Float())
		if ba != bb {
			*out = append(*out, r6Diff{"float64_bits", path, fmt.Sprintf("ref=%#016x (%g) got=%#016x (%g)", ba, ref.Float(), bb, got.Float())})
		}
	case reflect.String:
		rs, gs := ref.String(), got.String()
		if rs != gs {
			*out = append(*out, r6Diff{"string", path, fmt.Sprintf("ref=%q got=%q", rs, gs)})
		}
		if refTopo != nil && gotTopo != nil && len(rs) > 0 && len(gs) > 0 && refTopo.anchors.srcStart != 0 && gotTopo.anchors.srcStart != 0 {
			ra := uintptr(unsafe.Pointer(unsafe.StringData(rs)))
			ga := uintptr(unsafe.Pointer(unsafe.StringData(gs)))
			rBorrow := ra >= refTopo.anchors.srcStart && ra < refTopo.anchors.srcEnd
			gBorrow := ga >= gotTopo.anchors.srcStart && ga < gotTopo.anchors.srcEnd
			if rBorrow != gBorrow {
				*out = append(*out, r6Diff{"string_borrow", path, fmt.Sprintf("ref_borrows_src=%v got_borrows_src=%v", rBorrow, gBorrow)})
			}
		}
	case reflect.Ptr:
		if ref.IsNil() != got.IsNil() {
			*out = append(*out, r6Diff{"ptr_nil", path, fmt.Sprintf("ref_nil=%v got_nil=%v", ref.IsNil(), got.IsNil())})
			return
		}
		if ref.IsNil() {
			return
		}
		ra, ga := ref.Pointer(), got.Pointer()
		if refTopo != nil && gotTopo != nil {
			rTag := refTopo.tagAddr(ra, false)
			gTag := gotTopo.tagAddr(ga, false)
			if rTag != gTag {
				*out = append(*out, r6Diff{"ptr_alias", path, fmt.Sprintf("ref_tag=%s got_tag=%s", rTag, gTag)})
			}
		}
		pair := r6PtrPair{ra, ga}
		if seen[pair] {
			return
		}
		seen[pair] = true
		compareR6Values(path+".*", ref.Elem(), got.Elem(), refTopo, gotTopo, seen, out)
	case reflect.Interface:
		if ref.IsNil() != got.IsNil() {
			*out = append(*out, r6Diff{"iface_nil", path, fmt.Sprintf("ref_nil=%v got_nil=%v", ref.IsNil(), got.IsNil())})
			return
		}
		if !ref.IsNil() {
			compareR6Values(path+".<iface>", ref.Elem(), got.Elem(), refTopo, gotTopo, seen, out)
		}
	case reflect.Slice:
		if ref.IsNil() != got.IsNil() {
			*out = append(*out, r6Diff{"slice_nil", path, fmt.Sprintf("ref_nil=%v got_nil=%v", ref.IsNil(), got.IsNil())})
			return
		}
		if ref.Len() != got.Len() {
			*out = append(*out, r6Diff{"slice_len", path, fmt.Sprintf("ref_len=%d got_len=%d", ref.Len(), got.Len())})
			return
		}
		if ref.Cap() != got.Cap() {
			*out = append(*out, r6Diff{"slice_cap", path, fmt.Sprintf("ref_cap=%d got_cap=%d", ref.Cap(), got.Cap())})
		}
		if ref.Cap() > 0 && got.Cap() > 0 && refTopo != nil && gotTopo != nil {
			rTag := refTopo.tagAddr(ref.Pointer(), true)
			gTag := gotTopo.tagAddr(got.Pointer(), true)
			if rTag != gTag {
				*out = append(*out, r6Diff{"slice_backing_alias", path, fmt.Sprintf("ref_tag=%s got_tag=%s", rTag, gTag)})
			}
		}
		for i := 0; i < ref.Len(); i++ {
			compareR6Values(fmt.Sprintf("%s[%d]", path, i), ref.Index(i), got.Index(i), refTopo, gotTopo, seen, out)
		}
	case reflect.Array:
		for i := 0; i < ref.Len(); i++ {
			compareR6Values(fmt.Sprintf("%s[%d]", path, i), ref.Index(i), got.Index(i), refTopo, gotTopo, seen, out)
		}
	case reflect.Map:
		if ref.IsNil() != got.IsNil() {
			*out = append(*out, r6Diff{"map_nil", path, fmt.Sprintf("ref_nil=%v got_nil=%v", ref.IsNil(), got.IsNil())})
			return
		}
		if ref.Len() != got.Len() {
			*out = append(*out, r6Diff{"map_len", path, fmt.Sprintf("ref_len=%d got_len=%d", ref.Len(), got.Len())})
			return
		}
		keys := ref.MapKeys()
		sort.Slice(keys, func(i, j int) bool {
			return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface())
		})
		for _, k := range keys {
			gv := got.MapIndex(k)
			if !gv.IsValid() {
				*out = append(*out, r6Diff{"map_key", path, fmt.Sprintf("missing key %v in got", k.Interface())})
				continue
			}
			compareR6Values(fmt.Sprintf("%s[%v]", path, k.Interface()), ref.MapIndex(k), gv, refTopo, gotTopo, seen, out)
		}
	case reflect.Struct:
		rt := ref.Type()
		for i := 0; i < ref.NumField(); i++ {
			ft := rt.Field(i)
			if !ft.IsExported() {
				continue
			}
			compareR6Values(path+"."+ft.Name, ref.Field(i), got.Field(i), refTopo, gotTopo, seen, out)
		}
	default:
		if ref.CanInterface() && got.CanInterface() && !reflect.DeepEqual(ref.Interface(), got.Interface()) {
			*out = append(*out, r6Diff{"value", path, fmt.Sprintf("ref=%#v got=%#v", ref.Interface(), got.Interface())})
		}
	}
}

func contractErrorSrc(err error) (string, bool) {
	switch e := err.(type) {
	case SyntaxError:
		return e.Src, true
	case *SyntaxError:
		if e != nil {
			return e.Src, true
		}
	case MismatchTypeError:
		return e.Src, true
	case *MismatchTypeError:
		if e != nil {
			return e.Src, true
		}
	}
	return "", false
}

type r6SentinelExpect struct {
	sentinel error
	wantIs   bool
}

// r6ErrorContract specifies explicit sentinel identity expectations and wrapper
// comparison rules for user/callback error fixtures (N1 / §2.2).
// Ordinary wrappers compare by type, concrete fields, message, and unwrap tree
// structure without requiring identical wrapper pointer addresses; known
// sentinels are checked independently via errors.Is on both ref and got.
type r6ErrorContract struct {
	knownSentinels    []r6SentinelExpect
	forbidMultiUnwrap bool
	normalizeMsg      func(string) string
}

func errorNodePtrID(err error) (uintptr, bool) {
	if err == nil {
		return 0, false
	}
	rv := reflect.ValueOf(err)
	if rv.Kind() == reflect.Ptr && !rv.IsNil() {
		return rv.Pointer(), true
	}
	return 0, false
}

// verifyAcyclicBoundedErrorGraph validates that err's Unwrap() / Unwrap() []error
// graph is finite and acyclic BEFORE calling stdlib errors.Is (§2.3).
func verifyAcyclicBoundedErrorGraph(err error, maxNodes int) bool {
	if err == nil {
		return true
	}
	ancestors := make(map[uintptr]bool)
	visited := 0
	var walk func(cur error) bool
	walk = func(cur error) bool {
		if cur == nil {
			return true
		}
		visited++
		if visited > maxNodes {
			return false
		}
		ptr, hasPtr := errorNodePtrID(cur)
		if hasPtr {
			if ancestors[ptr] {
				return false
			}
			ancestors[ptr] = true
			defer delete(ancestors, ptr)
		}
		switch u := cur.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range u.Unwrap() {
				if !walk(child) {
					return false
				}
			}
		case interface{ Unwrap() error }:
			if !walk(u.Unwrap()) {
				return false
			}
		}
		return true
	}
	return walk(err)
}

func compareR6ErrorStructure(path string, refErr, gotErr error, contract *r6ErrorContract, depth int, out *[]r6Diff) {
	if depth > 16 {
		*out = append(*out, r6Diff{"err_depth", path, "unwrap depth exceeded 16"})
		return
	}
	if (refErr == nil) != (gotErr == nil) {
		*out = append(*out, r6Diff{"err_unwrap", path, fmt.Sprintf("ref=%v got=%v", refErr, gotErr)})
		return
	}
	if refErr == nil {
		return
	}
	if reflect.TypeOf(refErr) != reflect.TypeOf(gotErr) {
		*out = append(*out, r6Diff{"err_type", path, fmt.Sprintf("ref=%T (%v) got=%T (%v)", refErr, refErr, gotErr, gotErr)})
		return
	}
	rMsg, gMsg := refErr.Error(), gotErr.Error()
	if contract != nil && contract.normalizeMsg != nil {
		rMsg, gMsg = contract.normalizeMsg(rMsg), contract.normalizeMsg(gMsg)
	}
	if rMsg != gMsg {
		*out = append(*out, r6Diff{"err_msg", path, fmt.Sprintf("ref=%q got=%q", rMsg, gMsg)})
	}
	if !reflect.DeepEqual(refErr, gotErr) {
		*out = append(*out, r6Diff{"err_fields", path, fmt.Sprintf("ref=%#v got=%#v", refErr, gotErr)})
	}
	// Positional known-sentinel parity (N1 narrow edge case): when a fixture lists
	// multiple known sentinels (potentially with identical text, e.g. errors.Join(a, b)
	// vs errors.Join(b, a)), verify that refErr and gotErr match the same subset of
	// known sentinels at THIS node without applying root-level ks.wantIs to children
	// and without comparing ordinary wrapper pointers.
	if contract != nil && len(contract.knownSentinels) > 0 {
		for i, ks := range contract.knownSentinels {
			refAt := errors.Is(refErr, ks.sentinel)
			gotAt := errors.Is(gotErr, ks.sentinel)
			if refAt != gotAt {
				*out = append(*out, r6Diff{"err_sentinel_pos", fmt.Sprintf("%s.sentinel[%d]", path, i),
					fmt.Sprintf("sentinel=%q positional match mismatch: ref_is=%v got_is=%v", ks.sentinel, refAt, gotAt)})
			}
		}
	}

	rMulti, rHasMulti := refErr.(interface{ Unwrap() []error })
	gMulti, gHasMulti := gotErr.(interface{ Unwrap() []error })
	if rHasMulti || gHasMulti {
		if contract != nil && contract.forbidMultiUnwrap {
			*out = append(*out, r6Diff{"err_multi_unwrap", path, "multi-branch Unwrap() []error forbidden by contract"})
			return
		}
		if rHasMulti != gHasMulti {
			*out = append(*out, r6Diff{"err_unwrap", path, fmt.Sprintf("multi-unwrap presence mismatch: ref=%v got=%v", rHasMulti, gHasMulti)})
			return
		}
		rc, gc := rMulti.Unwrap(), gMulti.Unwrap()
		if len(rc) != len(gc) {
			*out = append(*out, r6Diff{"err_unwrap", path, fmt.Sprintf("multi-unwrap len mismatch: ref=%d got=%d", len(rc), len(gc))})
			return
		}
		for i := range rc {
			compareR6ErrorStructure(fmt.Sprintf("%s.unwrap[%d]", path, i), rc[i], gc[i], contract, depth+1, out)
		}
		return
	}

	ru, gu := errors.Unwrap(refErr), errors.Unwrap(gotErr)
	if ru != nil || gu != nil {
		compareR6ErrorStructure(fmt.Sprintf("%s.unwrap", path), ru, gu, contract, depth+1, out)
	}
}

func compareR6Errors(path string, refErr, gotErr error, out *[]r6Diff) {
	compareR6ErrorsWithContract(path, refErr, gotErr, nil, out)
}

func compareR6ErrorsWithContract(path string, refErr, gotErr error, contract *r6ErrorContract, out *[]r6Diff) {
	rk, gk := classifyContractErr(refErr), classifyContractErr(gotErr)
	if rk != gk {
		*out = append(*out, r6Diff{"err_kind", path, fmt.Sprintf("ref=%s (%T: %v) got=%s (%T: %v)", rk, refErr, refErr, gk, gotErr, gotErr)})
		return
	}
	if refErr == nil {
		if contract != nil {
			for i, ks := range contract.knownSentinels {
				if ks.wantIs {
					*out = append(*out, r6Diff{"err_sentinel", fmt.Sprintf("%s.sentinel[%d]", path, i), fmt.Sprintf("expected sentinel %v on nil error", ks.sentinel)})
				}
			}
		}
		return
	}
	if reflect.TypeOf(refErr) != reflect.TypeOf(gotErr) {
		*out = append(*out, r6Diff{"err_type", path, fmt.Sprintf("ref=%T (%v) got=%T (%v)", refErr, refErr, gotErr, gotErr)})
	}
	switch rk {
	case "syntax":
		rp, rm, rok := contractSyntaxDetail(refErr)
		gp, gm, gok := contractSyntaxDetail(gotErr)
		if rok != gok || rp != gp || rm != gm {
			*out = append(*out, r6Diff{"err_syntax", path, fmt.Sprintf("ref=(pos=%d msg=%q) got=(pos=%d msg=%q)", rp, rm, gp, gm)})
		}
		rs, rsok := contractErrorSrc(refErr)
		gs, gsok := contractErrorSrc(gotErr)
		if rsok != gsok || rs != gs {
			*out = append(*out, r6Diff{"err_src", path, fmt.Sprintf("ref_src_len=%d got_src_len=%d", len(rs), len(gs))})
		}
	case "mismatch":
		rp, rt, rok := contractMismatchDetail(refErr)
		gp, gt, gok := contractMismatchDetail(gotErr)
		if rok != gok || rp != gp || rt != gt {
			*out = append(*out, r6Diff{"err_mismatch", path, fmt.Sprintf("ref=(pos=%d type=%v) got=(pos=%d type=%v)", rp, rt, gp, gt)})
		}
		rs, rsok := contractErrorSrc(refErr)
		gs, gsok := contractErrorSrc(gotErr)
		if rsok != gsok || rs != gs {
			*out = append(*out, r6Diff{"err_src", path, fmt.Sprintf("ref_src_len=%d got_src_len=%d", len(rs), len(gs))})
		}
	default:
		// N1 / T6: First verify both unwrap graphs are finite and acyclic before calling errors.Is.
		if !verifyAcyclicBoundedErrorGraph(refErr, 64) {
			*out = append(*out, r6Diff{"err_graph_cycle", path + ".ref", "ref error unwrap graph cyclic or >64 nodes"})
			return
		}
		if !verifyAcyclicBoundedErrorGraph(gotErr, 64) {
			*out = append(*out, r6Diff{"err_graph_cycle", path + ".got", "got error unwrap graph cyclic or >64 nodes"})
			return
		}
		// Independent sentinel identity verification (covers top-level, nested, multi-wrap, and custom Is).
		if contract != nil {
			for i, ks := range contract.knownSentinels {
				refIs := errors.Is(refErr, ks.sentinel)
				gotIs := errors.Is(gotErr, ks.sentinel)
				if refIs != ks.wantIs || gotIs != ks.wantIs {
					*out = append(*out, r6Diff{"err_sentinel", fmt.Sprintf("%s.sentinel[%d]", path, i),
						fmt.Sprintf("sentinel=%q want_is=%v ref_is=%v got_is=%v", ks.sentinel, ks.wantIs, refIs, gotIs)})
				}
			}
		}
		// Structural equivalence for wrappers/concrete errors (freshly allocated equal wrappers PASS).
		compareR6ErrorStructure(path, refErr, gotErr, contract, 0, out)
	}
}

type r6StateBundle[T any] struct {
	dst     T
	extA    int64
	extB    int64
	backing [4]int
	rawSlot json.RawMessage
	logs    []string
}

func bundleAnchors[T any](b *r6StateBundle[T], srcBuf []byte) r6BundleAnchors {
	var a r6BundleAnchors
	if b != nil {
		a.extAAddr = uintptr(unsafe.Pointer(&b.extA))
		a.extBAddr = uintptr(unsafe.Pointer(&b.extB))
		a.backingStart = uintptr(unsafe.Pointer(&b.backing[0]))
		a.backingEnd = a.backingStart + unsafe.Sizeof(b.backing)
		a.rawSlotAddr = uintptr(unsafe.Pointer(&b.rawSlot))
		a.logsAddr = uintptr(unsafe.Pointer(&b.logs))
		a.dstStart = uintptr(unsafe.Pointer(&b.dst))
		a.dstEnd = a.dstStart + unsafe.Sizeof(b.dst)
	}
	if len(srcBuf) > 0 {
		a.srcStart = uintptr(unsafe.Pointer(unsafe.SliceData(srcBuf)))
		a.srcEnd = a.srcStart + uintptr(len(srcBuf))
	}
	return a
}

type r6RouteOutcome[T any] struct {
	name    string
	bundle  *r6StateBundle[T]
	endPos  int
	witness routeWitness
	err     error
}

func compareR6StateBundles[T any](
	routeName string,
	ref *r6StateBundle[T],
	got *r6StateBundle[T],
	refErr, gotErr error,
	refEnd, gotEnd int,
	checkEnd bool,
	srcBuf []byte,
) []r6Diff {
	return compareR6StateBundlesWithContract(routeName, ref, got, refErr, gotErr, refEnd, gotEnd, checkEnd, srcBuf, nil)
}

func compareR6StateBundlesWithContract[T any](
	routeName string,
	ref *r6StateBundle[T],
	got *r6StateBundle[T],
	refErr, gotErr error,
	refEnd, gotEnd int,
	checkEnd bool,
	srcBuf []byte,
	contract *r6ErrorContract,
) []r6Diff {
	var diffs []r6Diff
	compareR6ErrorsWithContract(routeName+".err", refErr, gotErr, contract, &diffs)
	if checkEnd && refEnd != gotEnd {
		diffs = append(diffs, r6Diff{"end_pos", routeName, fmt.Sprintf("ref=%d got=%d", refEnd, gotEnd)})
	}
	if ref.extA != got.extA {
		diffs = append(diffs, r6Diff{"ext_state", routeName + ".extA", fmt.Sprintf("ref=%d got=%d", ref.extA, got.extA)})
	}
	if ref.extB != got.extB {
		diffs = append(diffs, r6Diff{"ext_state", routeName + ".extB", fmt.Sprintf("ref=%d got=%d", ref.extB, got.extB)})
	}
	if ref.backing != got.backing {
		diffs = append(diffs, r6Diff{"ext_state", routeName + ".backing", fmt.Sprintf("ref=%v got=%v", ref.backing, got.backing)})
	}
	if (ref.rawSlot == nil) != (got.rawSlot == nil) || len(ref.rawSlot) != len(got.rawSlot) || cap(ref.rawSlot) != cap(got.rawSlot) || !bytes.Equal(ref.rawSlot, got.rawSlot) {
		diffs = append(diffs, r6Diff{"ext_state", routeName + ".rawSlot", fmt.Sprintf("ref=(nil=%v cap=%d %q) got=(nil=%v cap=%d %q)",
			ref.rawSlot == nil, cap(ref.rawSlot), string(ref.rawSlot), got.rawSlot == nil, cap(got.rawSlot), string(got.rawSlot))})
	}
	if (ref.logs == nil) != (got.logs == nil) || !reflect.DeepEqual(ref.logs, got.logs) {
		diffs = append(diffs, r6Diff{"callbacks", routeName + ".logs", fmt.Sprintf("ref=%#v got=%#v", ref.logs, got.logs)})
	}

	refTopo := newR6TopologyTracker(bundleAnchors(ref, srcBuf))
	gotTopo := newR6TopologyTracker(bundleAnchors(got, srcBuf))
	seen := make(map[r6PtrPair]bool, 8)
	compareR6Values(routeName+".dst", reflect.ValueOf(ref.dst), reflect.ValueOf(got.dst), refTopo, gotTopo, seen, &diffs)

	if len(diffs) == 0 && !reflect.DeepEqual(ref.dst, got.dst) {
		diffs = append(diffs, r6Diff{"deep_equal", routeName + ".dst", fmt.Sprintf("reflect.DeepEqual failed: ref=%#v got=%#v", ref.dst, got.dst)})
	}
	return diffs
}

func checkR6WitnessSanity(routeName string, rep decodeRepChoice, w routeWitness, expectIndexScan bool) []r6Diff {
	var diffs []r6Diff
	if rep == repForceTape && (w.IndexScanStarted != 0 || w.IndexBinderEntered != 0) {
		diffs = append(diffs, r6Diff{"witness_route", routeName, fmt.Sprintf("repForceTape entered index path: %+v", w)})
	}
	if expectIndexScan && w.IndexScanStarted == 0 {
		diffs = append(diffs, r6Diff{"witness_route", routeName, fmt.Sprintf("expected index scan start, got %+v", w)})
	}
	return diffs
}

func formatR6Diffs(diffs []r6Diff) string {
	var sb strings.Builder
	for i, d := range diffs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("  - ")
		sb.WriteString(d.String())
	}
	return sb.String()
}

func compareFiveWayOutcomes[T any](
	ref r6RouteOutcome[T],
	others []r6RouteOutcome[T],
	checkEnd bool,
	srcBuf []byte,
) []r6Diff {
	return compareFiveWayOutcomesWithContract(ref, others, checkEnd, srcBuf, nil)
}

func compareFiveWayOutcomesWithContract[T any](
	ref r6RouteOutcome[T],
	others []r6RouteOutcome[T],
	checkEnd bool,
	srcBuf []byte,
	contract *r6ErrorContract,
) []r6Diff {
	var all []r6Diff
	for _, o := range others {
		ds := compareR6StateBundlesWithContract(o.name, ref.bundle, o.bundle, ref.err, o.err, ref.endPos, o.endPos, checkEnd, srcBuf, contract)
		all = append(all, ds...)
	}
	return all
}

func decodeWithRep[T any](s string, f uint64, rep decodeRepChoice, forceScalar bool, initFn func(*r6StateBundle[T])) r6RouteOutcome[T] {
	b := new(r6StateBundle[T])
	b.extA = 111
	b.extB = -222
	b.backing = [4]int{10, 20, 30, 40}
	if initFn != nil {
		initFn(b)
	}
	var w routeWitness
	sOut, endPos, err := decodeOneWithControl(s, f, &b.dst, &decodeTestControl{
		forceRep:    rep,
		forceScalar: forceScalar,
		witness:     &w,
	})
	err = checkSingleDocEnd(sOut, endPos, err)
	return r6RouteOutcome[T]{
		bundle:  b,
		endPos:  endPos,
		witness: w,
		err:     err,
	}
}

func checkIndexVsTapeFourWay[T any](t *testing.T, s string, f uint64, checkStd bool, initFn func(*r6StateBundle[T])) routeWitness {
	t.Helper()
	w, _, _ := checkIndexVsTapeFiveWayWithContract[T](t, s, f, checkStd, initFn, nil)
	return w
}

func checkIndexVsTapeFiveWayWithContract[T any](
	t *testing.T,
	s string,
	f uint64,
	checkStd bool,
	initFn func(*r6StateBundle[T]),
	errContract *r6ErrorContract,
) (routeWitness, []r6RouteOutcome[T], []byte) {
	t.Helper()
	r6FuzzExecs.Add(1)

	origBuf := []byte(s)
	callerBuf := append([]byte(nil), origBuf...)
	sCaller := unsafe.String(unsafe.SliceData(callerBuf), len(callerBuf))

	// Snapshot untouched initial state to prove SYN-PREMUT on syntax errors.
	untouched := new(r6StateBundle[T])
	untouched.extA = 111
	untouched.extB = -222
	untouched.backing = [4]int{10, 20, 30, 40}
	if initFn != nil {
		initFn(untouched)
	}

	oAuto := decodeWithRep[T](sCaller, f, repAuto, false, initFn)
	oAuto.name = "auto"
	oIdxNat := decodeWithRep[T](sCaller, f, repForceIndex, false, initFn)
	oIdxNat.name = "idx_native"
	oIdxSca := decodeWithRep[T](sCaller, f, repForceIndex, true, initFn)
	oIdxSca.name = "idx_scalar"
	oTapeNat := decodeWithRep[T](sCaller, f, repForceTape, false, initFn)
	oTapeNat.name = "tape_native"
	oTapeSca := decodeWithRep[T](sCaller, f, repForceTape, true, initFn)
	oTapeSca.name = "tape_scalar"

	if !bytes.Equal(callerBuf, origBuf) {
		t.Fatalf("%T caller buffer mutated on %q", untouched.dst, s)
	}

	r6FuzzIndexScans.Add(uint64(oIdxNat.witness.IndexScanStarted + oIdxSca.witness.IndexScanStarted))
	r6FuzzIndexBinds.Add(uint64(oIdxNat.witness.IndexBinderEntered + oIdxSca.witness.IndexBinderEntered))
	r6FuzzSubtreeHits.Add(uint64(oIdxNat.witness.IndexSubtreeFallback + oIdxSca.witness.IndexSubtreeFallback))

	assertR6RouteProducer(t, "auto", false, oAuto.witness)
	assertR6RouteProducer(t, "idx_native", false, oIdxNat.witness)
	assertR6RouteProducer(t, "idx_scalar", true, oIdxSca.witness)
	assertR6RouteProducer(t, "tape_native", false, oTapeNat.witness)
	assertR6RouteProducer(t, "tape_scalar", true, oTapeSca.witness)
	recordR6ObservedProducers(oAuto.witness.ObservedProducers)
	recordR6ObservedProducers(oIdxNat.witness.ObservedProducers | oIdxNat.witness.PositionProducers | oIdxNat.witness.SubtreeProducers)
	recordR6ObservedProducers(oIdxSca.witness.ObservedProducers | oIdxSca.witness.PositionProducers | oIdxSca.witness.SubtreeProducers)
	recordR6ObservedProducers(oTapeNat.witness.ObservedProducers)
	recordR6ObservedProducers(oTapeSca.witness.ObservedProducers)

	if wd := checkR6WitnessSanity("tape_native", repForceTape, oTapeNat.witness, false); len(wd) > 0 {
		t.Fatalf("%T witness sanity failed on %q:\n%s", untouched.dst, s, formatR6Diffs(wd))
	}
	if wd := checkR6WitnessSanity("tape_scalar", repForceTape, oTapeSca.witness, false); len(wd) > 0 {
		t.Fatalf("%T witness sanity failed on %q:\n%s", untouched.dst, s, formatR6Diffs(wd))
	}

	others := []r6RouteOutcome[T]{oAuto, oIdxNat, oIdxSca, oTapeSca}
	noValSkip := (f & (1 << _F_no_validate_json)) != 0
	if !noValSkip {
		wantKind := classifyContractErr(oTapeNat.err)
		if wantKind == "syntax" {
			// Note: trailing garbage after a syntactically valid prefix fails in checkSingleDocEnd AFTER
			// DecodeOne succeeds; pure in-value syntax errors must never enter IndexBinderEntered.
			if oIdxNat.witness.IndexBinderEntered == 0 {
				r6FuzzSyntaxBlocks.Add(1)
				if diffs := compareR6StateBundles("syn_premut_idx_native", untouched, oIdxNat.bundle, nil, nil, 0, 0, false, callerBuf); len(diffs) > 0 {
					t.Fatalf("%T SYN-PREMUT violated on syntax error %v for %q:\n%s",
						untouched.dst, oIdxNat.err, s, formatR6Diffs(diffs))
				}
			}
		}
		// T7: All 5 routes execute decodeOneWithControl in the identical original-s coordinate domain,
		// so endPos MUST match across all 5 routes even on errors and UTF-8-repaired inputs!
		if diffs := compareFiveWayOutcomesWithContract(oTapeNat, others, true, callerBuf, errContract); len(diffs) > 0 {
			t.Fatalf("%T 5-route state/error mismatch (f=0x%x) on %q:\n%s",
				untouched.dst, f, s, formatR6Diffs(diffs))
		}
	} else {
		// NoValidateJSON policy (§6.4):
		// - Valid JSON (whether succeeding or returning a type-mismatch/callback error)
		//   MUST pass the full 5-route state + error + alias comparator.
		// - Only genuinely invalid JSON under explicit NoValidateJSON UB policy is
		//   exempted and explicitly counted (never silently treated as verified equal).
		if oTapeNat.err == nil || json.Valid([]byte(s)) {
			if diffs := compareFiveWayOutcomesWithContract(oTapeNat, others, true, callerBuf, errContract); len(diffs) > 0 {
				t.Fatalf("%T NoValidateJSON 5-route mismatch (f=0x%x) on %q:\n%s",
					untouched.dst, f, s, formatR6Diffs(diffs))
			}
		} else {
			r6FuzzNoValInvalidSkipped.Add(1)
		}
	}

	if checkStd && initFn == nil && utf8.ValidString(s) && !hasStdPre127StrconvFloatBug(s) {
		gotStd, errStd := decodeViaStdGenericWithOptsAndInit[T](s, f, nil)
		if (oTapeNat.err != nil) != (errStd != nil) {
			t.Fatalf("%T Sonic vs encoding/json err mismatch on %q: sonic=%v std=%v", untouched.dst, s, oTapeNat.err, errStd)
		}
		if errStd == nil {
			var stdDiffs []r6Diff
			compareR6Values("std", reflect.ValueOf(gotStd), reflect.ValueOf(oIdxNat.bundle.dst), nil, nil, make(map[r6PtrPair]bool), &stdDiffs)
			// Ignore capacity-only differences against encoding/json (Go stdlib uses append growth).
			filtered := stdDiffs[:0]
			for _, d := range stdDiffs {
				if d.Category == "slice_cap" {
					continue
				}
				filtered = append(filtered, d)
			}
			if len(filtered) > 0 || !reflect.DeepEqual(oIdxNat.bundle.dst, gotStd) {
				t.Fatalf("%T Index vs encoding/json value mismatch on %q:\n%s\n  idx=%#v\n  std=%#v",
					untouched.dst, s, formatR6Diffs(filtered), oIdxNat.bundle.dst, gotStd)
			}
		}
	}
	allOutcomes := []r6RouteOutcome[T]{oTapeNat, oAuto, oIdxNat, oIdxSca, oTapeSca}
	return oIdxNat.witness, allOutcomes, callerBuf
}

func r6IdxStructInits() []func(*r6StateBundle[r6IdxStruct]) {
	return []func(*r6StateBundle[r6IdxStruct]){
		nil,
		// Prefilled independent pointers + slice backing
		func(b *r6StateBundle[r6IdxStruct]) {
			s := "prefilled"
			b.dst.ID = -99
			b.dst.F64 = 1.5
			b.dst.PtrInt = &b.extA
			b.dst.PtrInt2 = &b.extB
			b.dst.PtrStr = &s
			b.dst.Nums = b.backing[:2:4]
		},
		// Shared pointer alias (PtrInt and PtrInt2 alias extA) + overlapping slice backing
		func(b *r6StateBundle[r6IdxStruct]) {
			b.dst.PtrInt = &b.extA
			b.dst.PtrInt2 = &b.extA
			b.dst.Nums = b.backing[:2:4]
			b.dst.Nums2 = b.backing[:2:4]
		},
		// Self-referential pointer (PtrInt points directly into dst.ID)
		func(b *r6StateBundle[r6IdxStruct]) {
			b.dst.ID = 777
			b.dst.PtrInt = &b.dst.ID
		},
	}
}

func r6DynamicStructInits() []func(*r6StateBundle[r6IdxDynamicStruct]) {
	return []func(*r6StateBundle[r6IdxDynamicStruct]){
		nil,
		// I5: Prefilled external pointer, RawMessage, and dynamic Unmarshaler inside Any / Anys
		func(b *r6StateBundle[r6IdxDynamicStruct]) {
			b.dst.Any = &r6DynCallbackProbe{V: 5, Log: &b.logs}
			pInt := int(b.extA)
			b.dst.Anys = []any{
				&pInt,
				&b.rawSlot,
				&r6DynCallbackProbe{V: 6, Log: &b.logs},
			}
			b.dst.Nums = b.backing[:2:4]
		},
		// I6: Callback mutating sibling field + duplicate key / null overwrite sensitivity
		func(b *r6StateBundle[r6IdxDynamicStruct]) {
			b.dst.Sibling = 10
			b.dst.Any = &r6DynCallbackProbe{
				V:          1,
				MutSibling: &b.dst.Sibling,
				Log:        &b.logs,
			}
		},
	}
}

func verifyR6WorkerWitnessOrFatal(tb testing.TB) {
	tb.Helper()
	// 1. Valid seed witness: must hit IndexScanStarted, IndexBarrierPassed, IndexBinderEntered.
	var validDst r6IdxStruct
	var wValid routeWitness
	_, _, err := decodeOneWithControl(`{"id":42,"f32":1.5,"f64":-0.0,"name":"ok","nums":[1,2]}`, diffStdOpts, &validDst, &decodeTestControl{
		forceRep: repForceIndex,
		witness:  &wValid,
	})
	if err != nil || wValid.IndexScanStarted != 1 || wValid.IndexBarrierPassed != 1 || wValid.IndexBinderEntered != 1 {
		tb.Fatalf("R6 fail-closed valid witness failed: err=%v witness=%+v", err, wValid)
	}
	if !math.Signbit(validDst.F64) {
		tb.Fatalf("R6 fail-closed -0.0 witness lost negative zero")
	}

	// 2. Malformed seed witness: must hit IndexScanStarted, NEVER IndexBarrierPassed or IndexBinderEntered or callback.
	var cbLogs []string
	malDst := r6IdxDynamicStruct{
		ID:  99,
		Any: &r6DynCallbackProbe{V: 7, Log: &cbLogs},
	}
	var wBad routeWitness
	_, _, errBad := decodeOneWithControl(`{"any":{"v":1},"id":123,"broken":`, diffStdOpts, &malDst, &decodeTestControl{
		forceRep: repForceIndex,
		witness:  &wBad,
	})
	if errBad == nil || wBad.IndexScanStarted != 1 || wBad.IndexBarrierPassed != 0 || wBad.IndexBinderEntered != 0 || malDst.ID != 99 || len(cbLogs) != 0 {
		tb.Fatalf("R6 fail-closed malformed witness failed: err=%v witness=%+v id=%d logs=%v",
			errBad, wBad, malDst.ID, cbLogs)
	}
}

func TestR6_IndexTargetMatrixAndBarrierWitness(t *testing.T) {
	verifyR6WorkerWitnessOrFatal(t)

	// Verify static compile-time admission for I1-I6 vs exclusion for T1:
	var i1 r6IdxStruct
	var i2a []r6IdxStruct
	var i2b []*r6IdxStruct
	var i3a []int
	var i3b []float32
	var i3c []string
	var i4 r6IdxDynamicStruct
	for name, ptr := range map[string]any{
		"I1_struct":     &i1,
		"I2_slice":      &i2a,
		"I2_ptr_slice":  &i2b,
		"I3_slice_int":  &i3a,
		"I3_slice_f32":  &i3b,
		"I3_slice_str":  &i3c,
		"I4_dyn_struct": &i4,
	} {
		_, _, _, proj, err := unpackAndCompileRoot(ptr)
		if err != nil || proj == nil || proj.idxPlan == nil {
			t.Fatalf("%s expected non-nil idxPlan, got proj=%+v err=%v", name, proj, err)
		}
	}

	// T1: Excluded types must NOT have idxPlan and must route to Tape/Schema, never Index.
	var t1Flat diffFlatStruct
	var t1Rich diffRichStruct
	for name, ptr := range map[string]any{
		"T1_fixed_array_diffFlatStruct": &t1Flat,
		"T1_map_strOpt_diffRichStruct":  &t1Rich,
	} {
		_, _, _, proj, err := unpackAndCompileRoot(ptr)
		if err != nil || (proj != nil && proj.idxPlan != nil) {
			t.Fatalf("%s expected idxPlan==nil, got proj=%+v", name, proj)
		}
		var w routeWitness
		_, _, _ = decodeOneWithControl(`{"id":1,"name":"t"}`, diffStdOpts, ptr, &decodeTestControl{
			forceRep: repForceIndex, // must NOT bypass unsupported static plan!
			witness:  &w,
		})
		if w.IndexScanStarted != 0 || w.IndexBinderEntered != 0 {
			t.Fatalf("%s unexpectedly entered index path: %+v", name, w)
		}
	}

	// Run deterministic corpus across I1, I2, I3, I4/I5/I6, S1, S2:
	detSeeds := []string{
		`{"id":42,"u64":99,"i32":-7,"f32":1.0000000596046448,"f64":-0.0,"flag":true,"name":"hello\n\u4e2d","ptr_int":8,"ptr_int2":null,"ptr_str":"p","nums":[1,2,3],"nums2":[],"f32s":[-0.0,3.5],"tags":["a","b"],"kids":[{"code":1,"tag":"k"}],"child":{"code":2,"tag":"c"}}`,
		`{"id":1,"id":2,"f64":-0.0,"any":{"v":11},"anys":[{"v":22},{"raw":[1,true]},{"v":33}],"sibling":5,"nums":[9]}`,
		`{"any":null,"any":{"v":77},"anys":[[1e400], {"x":[1e400],"ok":1}, "tail"],"name":"partial"}`,
		`{"any":{"v":1},"id":"bad_int","f64":-0.0,"name":"after_err"}`,
		`{"any":{"v":1},"id":10,"broken":[1,2}`,
	}
	for _, s := range detSeeds {
		for _, initFn := range r6IdxStructInits() {
			w := checkIndexVsTapeFourWay[r6IdxStruct](t, s, diffStdOpts, true, initFn)
			if w.IndexScanStarted == 0 {
				t.Fatalf("expected IndexScanStarted > 0 on I1 seed %q", s)
			}
		}
		for _, initFn := range r6DynamicStructInits() {
			w := checkIndexVsTapeFourWay[r6IdxDynamicStruct](t, s, diffStdOpts, true, initFn)
			if w.IndexScanStarted == 0 {
				t.Fatalf("expected IndexScanStarted > 0 on I4/I5/I6 seed %q", s)
			}
		}
	}

	// I2 & I3 deterministic slice checks:
	_ = checkIndexVsTapeFourWay[[]r6IdxStruct](t, `[{"id":1,"f64":-0.0},{"id":2,"name":"two"}]`, diffStdOpts, true, nil)
	_ = checkIndexVsTapeFourWay[[]*r6IdxStruct](t, `[{"id":1},null,{"id":3,"f32":-0.0}]`, diffStdOpts, true, nil)
	_ = checkIndexVsTapeFourWay[[]int](t, `[1,-2,3,0,99]`, diffStdOpts, true, nil)
	_ = checkIndexVsTapeFourWay[[]int64](t, `[9007199254740993,-1]`, diffStdOpts, true, nil)
	_ = checkIndexVsTapeFourWay[[]float32](t, `[-0.0,1.0000000596046448,3.25]`, diffStdOpts, true, nil)
	_ = checkIndexVsTapeFourWay[[]float64](t, `[-0.0,0.0,1e20]`, diffStdOpts, true, nil)
	_ = checkIndexVsTapeFourWay[[]string](t, `["plain","e\u0073c",""]`, diffStdOpts, true, nil)

	// NoValidateJSON deterministic 5-route check on valid JSON (both success & type mismatch):
	_ = checkIndexVsTapeFourWay[r6IdxStruct](t, detSeeds[0], diffNoValValidOpts, false, r6IdxStructInits()[1])
	_ = checkIndexVsTapeFourWay[r6IdxDynamicStruct](t, detSeeds[1], diffNoValValidOpts, false, r6DynamicStructInits()[1])
	_ = checkIndexVsTapeFourWay[r6IdxDynamicStruct](t, detSeeds[3], diffNoValValidOpts, false, r6DynamicStructInits()[1])

	// S1: Multi-value string session at non-zero offsets hitting ValidatedIndex across ALL 5 routes
	// with prefilled external pointers and shared backing storage (F2 / §6.5).
	multiSrc := `  {"id":10,"name":"first","f32":-0.0,"f64":-0.0,"ptr_int":41,"nums":[1,2]}   {"id":20,"name":"second","ptr_int2":82,"nums":[7,8,9]}  `
	type s1Route struct {
		name        string
		rep         decodeRepChoice
		forceScalar bool
		expectIdx   bool
		pos         int
		sess        *InputSession
	}
	s1Routes := []s1Route{
		{name: "auto", rep: repAuto, forceScalar: false, expectIdx: true},
		{name: "idx_native", rep: repForceIndex, forceScalar: false, expectIdx: true},
		{name: "idx_scalar", rep: repForceIndex, forceScalar: true, expectIdx: true},
		{name: "tape_native", rep: repForceTape, forceScalar: false, expectIdx: false},
		{name: "tape_scalar", rep: repForceTape, forceScalar: true, expectIdx: false},
	}
	s1Init := r6IdxStructInits()[1]
	for step := 0; step < 2; step++ {
		var outcomes [5]r6RouteOutcome[r6IdxStruct]
		for ri := range s1Routes {
			r := &s1Routes[ri]
			b := new(r6StateBundle[r6IdxStruct])
			b.extA = 111
			b.extB = -222
			b.backing = [4]int{10, 20, 30, 40}
			s1Init(b)
			var w routeWitness
			srcCopy := multiSrc
			err := decodeSessionWithControl(&srcCopy, &r.pos, diffStdOpts, &b.dst, &r.sess, &decodeTestControl{
				forceRep:    r.rep,
				forceScalar: r.forceScalar,
				witness:     &w,
			})
			if r.expectIdx && w.IndexBinderEntered != 1 {
				t.Fatalf("S1 step %d route %s expected IndexBinderEntered==1, got %+v", step, r.name, w)
			}
			if !r.expectIdx && (w.IndexScanStarted != 0 || w.IndexBinderEntered != 0) {
				t.Fatalf("S1 step %d route %s unexpectedly entered index path: %+v", step, r.name, w)
			}
			outcomes[ri] = r6RouteOutcome[r6IdxStruct]{
				name:    r.name,
				bundle:  b,
				endPos:  r.pos,
				witness: w,
				err:     err,
			}
		}
		ref := outcomes[3] // tape_native
		if ref.err != nil {
			t.Fatalf("S1 step %d tape_native failed: %v", step, ref.err)
		}
		others := []r6RouteOutcome[r6IdxStruct]{outcomes[0], outcomes[1], outcomes[2], outcomes[4]}
		if diffs := compareFiveWayOutcomes(ref, others, true, nil); len(diffs) > 0 {
			t.Fatalf("S1 step %d 5-route mismatch:\n%s", step, formatR6Diffs(diffs))
		}
	}

	// S2: Chunked io.Reader Stream route parity against Index using full comparator:
	streamJSON := `{"id":55,"f32":-0.0,"f64":-0.0,"name":"streamed","nums":[4,5]}`
	gotStream, _, errSt := decodeViaChunkedStreamPosWithInit[r6IdxStruct](streamJSON, diffStdOpts, 5, nil)
	oIdx := decodeWithRep[r6IdxStruct](streamJSON, diffStdOpts, repForceIndex, false, nil)
	if errSt != nil || oIdx.err != nil || oIdx.witness.IndexBinderEntered != 1 {
		t.Fatalf("S2 stream vs index err: errSt=%v errIdx=%v w=%+v", errSt, oIdx.err, oIdx.witness)
	}
	var s2Diffs []r6Diff
	compareR6Values("S2", reflect.ValueOf(gotStream), reflect.ValueOf(oIdx.bundle.dst), nil, nil, make(map[r6PtrPair]bool), &s2Diffs)
	if len(s2Diffs) > 0 || !reflect.DeepEqual(gotStream, oIdx.bundle.dst) {
		t.Fatalf("S2 stream vs index value mismatch:\n%s", formatR6Diffs(s2Diffs))
	}

	// N2 (§3.2): Pool-reuse stability on a FIXED pinnedParser with independent literal
	// expectations (NEVER a second decoded Tape snapshot, and NEVER sync.Pool luck).
	t.Run("N2_PinnedParserReuseStability", func(t *testing.T) {
		poolSrc := `{"id":101,"u64":202,"i32":-3,"f32":-0.0,"f64":-0.0,"flag":true,"name":"alpha\nbeta\t\u0041gamma","nums":[11,22,33]}`
		wantName := strings.Clone("alpha\nbeta\tAgamma")
		wantNums := []int{11, 22, 33}

		for _, rep := range []decodeRepChoice{repForceIndex, repForceTape} {
			fixedP := &Parser{
				Document: Document{
					nodes: make([]node, defaultNodesCount, defaultNodesCount),
				},
			}
			var firstDst r6IdxStruct
			var wFirst routeWitness
			_, endPos, err := decodeOneWithControl(poolSrc, diffStdOpts, &firstDst, &decodeTestControl{
				forceRep:     rep,
				pinnedParser: fixedP,
				witness:      &wFirst,
			})
			if err != nil || endPos != len(poolSrc) {
				t.Fatalf("rep=%d initial pinned decode failed: err=%v endPos=%d/%d", rep, err, endPos, len(poolSrc))
			}
			if rep == repForceIndex && wFirst.IndexBinderEntered != 1 {
				t.Fatalf("repForceIndex did not enter binder: %+v", wFirst)
			}

			// Actively reuse the EXACT SAME pinnedParser with varied escaped payloads to overwrite scratch!
			overwritePayloads := []string{
				`{"id":9991,"name":"` + strings.Repeat(`X\n`, 32) + `","nums":[9,8,7,6,5]}`,
				`{"id":9992,"name":"` + strings.Repeat(`Y\t\u005a`, 256) + `","nums":[1,2]}`,
				`{"id":9993,"name":"` + strings.Repeat(`Z\\`, 2048) + `","nums":[4,4,4,4,4,4]}`,
			}
			for round := 0; round < 16; round++ {
				var scratchDst r6IdxStruct
				ov := overwritePayloads[round%len(overwritePayloads)]
				if _, _, oerr := decodeOneWithControl(ov, diffStdOpts, &scratchDst, &decodeTestControl{
					forceRep:     rep,
					pinnedParser: fixedP,
				}); oerr != nil {
					t.Fatalf("rep=%d reuse round %d failed: %v", rep, round, oerr)
				}
			}

			// Verify earlier decoded value against independent literal expectations (not another decoded object!).
			if firstDst.ID != 101 || firstDst.U64 != 202 || firstDst.I32 != -3 || !firstDst.Flag ||
				math.Float32bits(firstDst.F32) != math.Float32bits(float32(math.Copysign(0, -1))) ||
				math.Float64bits(firstDst.F64) != math.Float64bits(math.Copysign(0, -1)) ||
				firstDst.Name != wantName || !reflect.DeepEqual(firstDst.Nums, wantNums) {
				t.Fatalf("rep=%d pinnedParser reuse mutated earlier decoded result: got=%+v wantName=%q wantNums=%v",
					rep, firstDst, wantName, wantNums)
			}
		}

		// Negative self-test (§3.2):
		//   1) Verify string-vs-scratch address-range probe strictly on an unmodified backing (never
		//      mutating bytes behind a live string): a probe string backed by an unmodified buffer
		//      must be flagged as overlapping, while decoded d1.Name must NOT overlap badP.unescBuf.
		//   2) Use a legal mutable []byte alias (rawScratch) against an independent []byte copy
		//      (wantBytes) to prove reusing badP deterministically overwrites badP.unescBuf.
		strOverlapsBuf := func(s string, b []byte) bool {
			if len(s) == 0 || cap(b) == 0 {
				return false
			}
			full := b[:cap(b)]
			p := uintptr(unsafe.Pointer(unsafe.StringData(s)))
			lo := uintptr(unsafe.Pointer(unsafe.SliceData(full)))
			hi := lo + uintptr(len(full))
			return p >= lo && p < hi
		}
		// Probe self-check on an immutable, never-mutated buffer.
		unmodifiedScratch := []byte(wantName)
		probeStr := unsafe.String(unsafe.SliceData(unmodifiedScratch), len(unmodifiedScratch))
		if !strOverlapsBuf(probeStr, unmodifiedScratch) {
			t.Fatal("strOverlapsBuf failed to detect string aliasing unmodified scratch")
		}

		badP := &Parser{Document: Document{nodes: make([]node, defaultNodesCount, defaultNodesCount)}}
		var d1 r6IdxStruct
		if _, _, err := decodeOneWithControl(poolSrc, diffStdOpts, &d1, &decodeTestControl{
			forceRep:     repForceIndex,
			pinnedParser: badP,
		}); err != nil {
			t.Fatalf("negative self-test setup failed: %v", err)
		}
		if cap(badP.unescBuf) < len(wantName) {
			t.Fatalf("expected non-empty retained badP.unescBuf backing after escaped Index decode, cap=%d", cap(badP.unescBuf))
		}
		if strOverlapsBuf(d1.Name, badP.unescBuf) {
			t.Fatalf("decoded d1.Name unexpectedly aliased badP.unescBuf backing")
		}
		// Mutable []byte alias + independent copy prove reusing badP overwrites its retained scratch.
		rawScratch := badP.unescBuf[:len(wantName)]
		copy(rawScratch, wantName)
		wantBytes := []byte(wantName)
		if !bytes.Equal(rawScratch, wantBytes) {
			t.Fatalf("negative self-test precondition failed: %q != %q", string(rawScratch), string(wantBytes))
		}
		var d2 r6IdxStruct
		if _, _, err := decodeOneWithControl(`{"id":2,"name":"ZZZZZZZZZZZZZZZZ\nZZZZ"}`, diffStdOpts, &d2, &decodeTestControl{
			forceRep:     repForceIndex,
			pinnedParser: badP,
		}); err != nil {
			t.Fatalf("negative self-test overwrite decode failed: %v", err)
		}
		if bytes.Equal(rawScratch, wantBytes) {
			t.Fatalf("negative self-test failed: reusing pinnedParser did not overwrite its retained unescBuf scratch (%q)", string(rawScratch))
		}
		if d1.Name != wantName {
			t.Fatalf("pinnedParser reuse corrupted previously decoded d1.Name: got=%q want=%q", d1.Name, wantName)
		}
	})

	// N2 (§3.1): RawMessage copy vs NoCopyRawMessage borrow/copy independent expectations across all 5 routes!
	t.Run("N2_NoCopyRawMessageIndependentContract", func(t *testing.T) {
		sliceInRange := func(b, base []byte) bool {
			if len(b) == 0 || len(base) == 0 {
				return false
			}
			p := uintptr(unsafe.Pointer(unsafe.SliceData(b)))
			lo := uintptr(unsafe.Pointer(unsafe.SliceData(base)))
			return p >= lo && p < lo+uintptr(len(base))
		}
		type testNoCopyRaw []byte
		prevNoCopy := noCopyRawMessageType
		prevNoCopyPtr := noCopyRawMessagePtrType
		RegisterNoCopyRawMessageType(reflect.TypeOf(testNoCopyRaw(nil)))
		defer func() {
			noCopyRawMessageType = prevNoCopy
			noCopyRawMessagePtrType = prevNoCopyPtr
		}()
		rawBorrowSrc := `{"id":7,"anys":[{"copied":1},{"borrowed":2}]}`
		const wantCopied = `{"copied":1}`
		const wantBorrowed = `{"borrowed":2}`
		for _, tc := range []struct {
			name         string
			opts         uint64
			expectBorrow bool
		}{
			{"CopyStringOff_BorrowsImmutableSource", 0, true},
			{"CopyStringOn_DetachesNoCopyRaw", 1 << _F_copy_string, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rawInit := func(b *r6StateBundle[r6IdxDynamicStruct]) {
					var nc testNoCopyRaw
					b.dst.Anys = []any{&b.rawSlot, &nc}
				}
				w, outcomes, srcBuf := checkIndexVsTapeFiveWayWithContract[r6IdxDynamicStruct](t, rawBorrowSrc, tc.opts, false, rawInit, nil)
				if w.IndexBinderEntered != 1 || w.IndexSubtreeFallback == 0 {
					t.Fatalf("RawMessage/NoCopyRawMessage test (opts=0x%x) expected index+subtree, got %+v", tc.opts, w)
				}
				// Independent contract assertions on EVERY single one of the 5 routes!
				for _, oc := range outcomes {
					if oc.err != nil {
						t.Fatalf("route %s expected err==nil, got %v", oc.name, oc.err)
					}
					if oc.bundle.dst.ID != 7 || len(oc.bundle.dst.Anys) != 2 {
						t.Fatalf("route %s unexpected dst: %+v", oc.name, oc.bundle.dst)
					}
					if string(oc.bundle.rawSlot) != wantCopied {
						t.Fatalf("route %s RawMessage content got %q want %q", oc.name, string(oc.bundle.rawSlot), wantCopied)
					}
					if sliceInRange(oc.bundle.rawSlot, srcBuf) {
						t.Fatalf("route %s RawMessage unexpectedly aliased input source buffer", oc.name)
					}
					ncPtr, ok := oc.bundle.dst.Anys[1].(*testNoCopyRaw)
					if !ok || ncPtr == nil {
						t.Fatalf("route %s Anys[1] lost *testNoCopyRaw type: %T", oc.name, oc.bundle.dst.Anys[1])
					}
					if string(*ncPtr) != wantBorrowed {
						t.Fatalf("route %s NoCopyRaw content got %q want %q", oc.name, string(*ncPtr), wantBorrowed)
					}
					gotBorrow := sliceInRange(*ncPtr, srcBuf)
					if gotBorrow != tc.expectBorrow {
						t.Fatalf("route %s NoCopyRaw borrow contract mismatch (opts=0x%x): aliased=%v want=%v",
							oc.name, tc.opts, gotBorrow, tc.expectBorrow)
					}
				}
			})
		}
	})
}

// TestF2_FullComparatorNegativeInjection proves that the unified 5-route comparator
// detects every required value, float-bit, nil/empty, external-state, callback,
// error, capacity/alias-topology, single-route, and witness divergence (§6.5).
func TestF2_FullComparatorNegativeInjection(t *testing.T) {
	makeBase := func() (*r6StateBundle[r6IdxStruct], []byte) {
		src := []byte(`{"id":42,"u64":99,"i32":-7,"f32":-0.0,"f64":-0.0,"flag":true,"name":"ok"}`)
		b := new(r6StateBundle[r6IdxStruct])
		b.extA = 111
		b.extB = -222
		b.backing = [4]int{10, 20, 30, 40}
		b.rawSlot = json.RawMessage(`{"k":1}`)
		b.logs = []string{"cb1", "cb2"}
		b.dst = r6IdxStruct{
			ID:      42,
			U64:     99,
			I32:     -7,
			F32:     float32(math.Copysign(0, -1)),
			F64:     math.Copysign(0, -1),
			Flag:    true,
			Name:    "ok",
			PtrInt:  &b.extA,
			PtrInt2: &b.extA,
			Nums:    b.backing[:2:4],
			Nums2:   []int{},
			F32s:    []float32{1.0000001},
		}
		return b, src
	}

	assertDetects := func(t *testing.T, wantCat string, diffs []r6Diff) {
		t.Helper()
		if len(diffs) == 0 {
			t.Fatalf("expected comparator to detect %q divergence, got 0 diffs", wantCat)
		}
		for _, d := range diffs {
			if d.Category == wantCat {
				return
			}
		}
		t.Fatalf("expected diff category %q, got:\n%s", wantCat, formatR6Diffs(diffs))
	}

	// Positive control: identical independent bundles produce zero diffs.
	ref0, src0 := makeBase()
	got0, _ := makeBase()
	if diffs := compareR6StateBundles("control", ref0, got0, nil, nil, 10, 10, true, src0); len(diffs) != 0 {
		t.Fatalf("identical bundles produced unexpected diffs:\n%s", formatR6Diffs(diffs))
	}

	t.Run("Int64Change", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		got.dst.ID = 43
		assertDetects(t, "int", compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src))
	})
	t.Run("Int32Change", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		got.dst.I32 = -8
		assertDetects(t, "int", compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src))
	})
	t.Run("Uint64Change", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		got.dst.U64 = 100
		assertDetects(t, "uint", compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src))
	})
	t.Run("BoolChange", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		got.dst.Flag = false
		assertDetects(t, "bool", compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src))
	})
	t.Run("StringChange", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		got.dst.Name = "different"
		assertDetects(t, "string", compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src))
	})
	t.Run("Float64PosVsNegZero", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		got.dst.F64 = 0.0
		assertDetects(t, "float64_bits", compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src))
	})
	t.Run("Float32PosVsNegZero", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		got.dst.F32 = 0.0
		assertDetects(t, "float32_bits", compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src))
	})
	t.Run("Float32SingleStepRounding", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		got.dst.F32s = []float32{math.Nextafter32(1.0000001, 2.0)}
		assertDetects(t, "float32_bits", compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src))
	})
	t.Run("NilVsEmptySlice", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		got.dst.Nums2 = nil
		assertDetects(t, "slice_nil", compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src))
	})
	t.Run("NilVsEmptyMap", func(t *testing.T) {
		type mapHolder struct{ M map[string]int }
		r := &r6StateBundle[mapHolder]{dst: mapHolder{M: map[string]int{}}}
		g := &r6StateBundle[mapHolder]{dst: mapHolder{M: nil}}
		assertDetects(t, "map_nil", compareR6StateBundles("got", r, g, nil, nil, 0, 0, false, nil))
	})
	t.Run("SingleRouteOnlyDivergence", func(t *testing.T) {
		for _, targetRoute := range []string{"auto", "idx_native", "idx_scalar", "tape_scalar"} {
			refB, src := makeBase()
			refO := r6RouteOutcome[r6IdxStruct]{name: "tape_native", bundle: refB, endPos: 10}
			others := make([]r6RouteOutcome[r6IdxStruct], 0, 4)
			for _, rn := range []string{"auto", "idx_native", "idx_scalar", "tape_scalar"} {
				b, _ := makeBase()
				if rn == targetRoute {
					b.dst.ID = 7777
				}
				others = append(others, r6RouteOutcome[r6IdxStruct]{name: rn, bundle: b, endPos: 10})
			}
			diffs := compareFiveWayOutcomes(refO, others, true, src)
			assertDetects(t, "int", diffs)
			if !strings.HasPrefix(diffs[0].Path, targetRoute+".") {
				t.Fatalf("expected diff path prefixed with %q, got %q", targetRoute, diffs[0].Path)
			}
		}
	})
	t.Run("ExternalVarDiffWithIdenticalDst", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		// Detach pointers in both dsts so dst looks identical, while extA & backing tail differ!
		ref.dst.PtrInt, ref.dst.PtrInt2, ref.dst.Nums = nil, nil, nil
		got.dst.PtrInt, got.dst.PtrInt2, got.dst.Nums = nil, nil, nil
		got.extA = 999
		got.backing[3] = 9999
		got.rawSlot = json.RawMessage(`{"k":2}`)
		diffs := compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src)
		assertDetects(t, "ext_state", diffs)
		if len(diffs) < 3 {
			t.Fatalf("expected extA + backing + rawSlot diffs, got:\n%s", formatR6Diffs(diffs))
		}
	})
	t.Run("CallbackOrderAndIdentity", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		got.logs = []string{"cb2", "cb1"}
		assertDetects(t, "callbacks", compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src))
	})
	t.Run("ErrorSentinelIdentity", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		s1 := errors.New("sentinel-A")
		s2 := errors.New("sentinel-B")
		e1 := fmt.Errorf("wrap: %w", s1)
		e2 := fmt.Errorf("wrap: %w", s2)
		assertDetects(t, "err_msg", compareR6StateBundles("got", ref, got, e1, e2, 10, 10, true, src))
	})
	t.Run("SliceCapAndBackingAlias", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		// Same elements [10, 20], but detached backing array & different cap (2 instead of 4)!
		got.dst.Nums = []int{10, 20}
		diffs := compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src)
		assertDetects(t, "slice_cap", diffs)
		assertDetects(t, "slice_backing_alias", diffs)
	})
	t.Run("PointerAliasTopology", func(t *testing.T) {
		ref, src := makeBase()
		got, _ := makeBase()
		// Same numeric value 111 in both PtrInt and PtrInt2, but PtrInt2 points to a separate int64 instead of aliasing extA!
		detached := int64(111)
		got.dst.PtrInt2 = &detached
		diffs := compareR6StateBundles("got", ref, got, nil, nil, 10, 10, true, src)
		assertDetects(t, "ptr_alias", diffs)
	})
	t.Run("RawMessageCopyVsBorrowAlias", func(t *testing.T) {
		type rawWrap struct {
			R json.RawMessage
		}
		src := []byte(`{"k":1}`)
		copied := append(json.RawMessage(nil), src...)
		borrowed := json.RawMessage(src)
		ref := &r6StateBundle[rawWrap]{dst: rawWrap{R: copied}}
		got := &r6StateBundle[rawWrap]{dst: rawWrap{R: borrowed}}
		assertDetects(t, "slice_backing_alias", compareR6StateBundles("got", ref, got, nil, nil, 7, 7, true, src))
	})
	t.Run("WitnessRouteMismatch", func(t *testing.T) {
		d1 := checkR6WitnessSanity("tape_native", repForceTape, routeWitness{IndexBinderEntered: 1}, false)
		assertDetects(t, "witness_route", d1)
		d2 := checkR6WitnessSanity("idx_native", repForceIndex, routeWitness{IndexScanStarted: 0}, true)
		assertDetects(t, "witness_route", d2)
	})
}

// ============================================================================
// T6 & T7 Targeted Tests (H17 / §10)
// ============================================================================

type t6CustomCBError struct {
	Code  int
	Field string
	Cause error
}

func (e *t6CustomCBError) Error() string { return "custom callback error" }
func (e *t6CustomCBError) Unwrap() error { return e.Cause }

type t6CallbackProbe struct {
	FailErr           error     `json:"-"`
	FreshWrapSentinel error     `json:"-"`
	FreshWrapCode     int       `json:"-"`
	FreshWrapField    string    `json:"-"`
	Log               *[]string `json:"-"`
	Val               int       `json:"v"`
}

func (p *t6CallbackProbe) UnmarshalJSON(b []byte) error {
	if p.Log != nil {
		*p.Log = append(*p.Log, string(b))
	}
	if p.FreshWrapSentinel != nil {
		return fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &t6CustomCBError{
			Code:  p.FreshWrapCode,
			Field: p.FreshWrapField,
			Cause: p.FreshWrapSentinel,
		}))
	}
	if p.FailErr != nil {
		return p.FailErr
	}
	var tmp struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	p.Val = tmp.V
	return nil
}

type n1UncomparableError struct {
	Code int
	Tags []string // slice field makes this concrete error type uncomparable under ==
}

func (e n1UncomparableError) Error() string { return "uncomparable custom error" }

type n1CustomIsError struct {
	Msg    string
	Target error
}

func (e *n1CustomIsError) Error() string        { return e.Msg }
func (e *n1CustomIsError) Is(target error) bool { return target == e.Target }

type n1CyclicError struct {
	Next error
}

func (e *n1CyclicError) Error() string { return "cyclic" }
func (e *n1CyclicError) Unwrap() error { return e.Next }

// TestT6_UserErrorIdentityUnwrapAndOrderParity verifies T6 & N1 (§2.2–§2.4):
//   - Known sentinel identity checks (top-level, nested, multi-wrap, custom Is)
//   - Structural equivalence of independently created multi-layer wrappers (no false positive!)
//   - Uncomparable custom error types (no interface == panic) & cyclic unwrap termination guard
//   - Live 5-route decode where callbacks freshly allocate 2-layer wrappers per invocation,
//     across BOTH error orders (callback-first & mismatch-first) preserving OptDec continue semantics.
func TestT6_UserErrorIdentityUnwrapAndOrderParity(t *testing.T) {
	sentinelA := errors.New("same-text-sentinel")
	sentinelB := errors.New("same-text-sentinel") // identical Error() text, distinct identity!

	contractA := &r6ErrorContract{
		knownSentinels: []r6SentinelExpect{
			{sentinel: sentinelA, wantIs: true},
			{sentinel: sentinelB, wantIs: false},
		},
	}

	// 1. Top-level distinct same-text sentinel MUST fail under contractA (fixes N1 false negative!).
	t.Run("N1_TopLevelDistinctSentinelDetected", func(t *testing.T) {
		var diffs []r6Diff
		compareR6ErrorsWithContract("err", sentinelA, sentinelB, contractA, &diffs)
		if len(diffs) == 0 || diffs[0].Category != "err_sentinel" {
			t.Fatalf("expected err_sentinel diff for top-level distinct sentinel, got %v", diffs)
		}
		// Even if BOTH routes drop sentinelA for fresh same-text error, independent wantIs catches it!
		fresh1, fresh2 := errors.New("same-text-sentinel"), errors.New("same-text-sentinel")
		var bothDropped []r6Diff
		compareR6ErrorsWithContract("err", fresh1, fresh2, contractA, &bothDropped)
		if len(bothDropped) == 0 || bothDropped[0].Category != "err_sentinel" {
			t.Fatalf("expected err_sentinel when both routes replace top-level sentinel, got %v", bothDropped)
		}
	})

	// 2. Independently created 2-layer fmt.Errorf wrappers around the SAME sentinel MUST PASS (fixes N1 false positive!).
	t.Run("N1_Fresh2LayerWrappersEquivalent", func(t *testing.T) {
		w1 := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", sentinelA))
		w2 := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", sentinelA))
		var diffs []r6Diff
		compareR6ErrorsWithContract("err", w1, w2, contractA, &diffs)
		if len(diffs) != 0 {
			t.Fatalf("false positive on independently created equivalent 2-layer wrappers:\n%s", formatR6Diffs(diffs))
		}
	})

	// 3. Same Error() text, different concrete user field MUST fail.
	t.Run("N1_ConcreteFieldMismatchDetected", func(t *testing.T) {
		var diffs []r6Diff
		compareR6ErrorsWithContract("err",
			&t6CustomCBError{Code: 1, Field: "a", Cause: sentinelA},
			&t6CustomCBError{Code: 2, Field: "a", Cause: sentinelA},
			contractA, &diffs)
		if len(diffs) == 0 {
			t.Fatal("failed to detect concrete error field difference with identical Error() text")
		}
	})

	// 4. Different wrapping depth or child nodes MUST fail.
	t.Run("N1_WrapDepthAndChildMismatchDetected", func(t *testing.T) {
		w1 := fmt.Errorf("outer: %w", sentinelA)
		w2 := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", sentinelA))
		var diffs []r6Diff
		compareR6ErrorsWithContract("err", w1, w2, contractA, &diffs)
		if len(diffs) == 0 {
			t.Fatal("failed to detect wrapping depth mismatch")
		}
		var sentDiffs []r6Diff
		compareR6ErrorsWithContract("err",
			&t6CustomCBError{Code: 1, Field: "a", Cause: sentinelA},
			&t6CustomCBError{Code: 1, Field: "a", Cause: sentinelB},
			contractA, &sentDiffs)
		if len(sentDiffs) == 0 {
			t.Fatal("failed to detect wrapped distinct sentinel identity")
		}
	})

	// 5. errors.Join multi-branch unwrap: independent sentinel matrix AND child order preservation.
	t.Run("N1_ErrorsJoinMatrixAndChildOrder", func(t *testing.T) {
		aux := errors.New("aux")
		joinContract := &r6ErrorContract{
			knownSentinels: []r6SentinelExpect{
				{sentinel: sentinelA, wantIs: true},
				{sentinel: sentinelB, wantIs: false},
				{sentinel: aux, wantIs: true},
			},
		}
		j1 := errors.Join(fmt.Errorf("wrap: %w", sentinelA), aux)
		j2 := errors.Join(fmt.Errorf("wrap: %w", sentinelA), aux)
		var okDiffs []r6Diff
		compareR6ErrorsWithContract("err", j1, j2, joinContract, &okDiffs)
		if len(okDiffs) != 0 {
			t.Fatalf("equivalent errors.Join failed:\n%s", formatR6Diffs(okDiffs))
		}
		// Wrong sentinel in Join
		jWrong := errors.Join(fmt.Errorf("wrap: %w", sentinelB), aux)
		var wrongDiffs []r6Diff
		compareR6ErrorsWithContract("err", j1, jWrong, joinContract, &wrongDiffs)
		if len(wrongDiffs) == 0 {
			t.Fatal("failed to detect wrong sentinel inside errors.Join")
		}
		// Swapped child order in Join must NOT be silently smoothed over
		jSwapped := errors.Join(aux, fmt.Errorf("wrap: %w", sentinelA))
		var orderDiffs []r6Diff
		compareR6ErrorsWithContract("err", j1, jSwapped, joinContract, &orderDiffs)
		if len(orderDiffs) == 0 {
			t.Fatal("failed to detect swapped child order inside errors.Join")
		}
	})

	// 5b. Same-text distinct-identity sentinels in errors.Join: Join(a, b) vs Join(b, a) MUST be detected!
	t.Run("N1_SameTextDistinctSentinelJoinOrderDetected", func(t *testing.T) {
		sameA := errors.New("same-join-sentinel")
		sameB := errors.New("same-join-sentinel")
		bothContract := &r6ErrorContract{
			knownSentinels: []r6SentinelExpect{
				{sentinel: sameA, wantIs: true},
				{sentinel: sameB, wantIs: true},
			},
		}
		// Same order -> equivalent (0 diffs).
		var eqDiffs []r6Diff
		compareR6ErrorsWithContract("err", errors.Join(sameA, sameB), errors.Join(sameA, sameB), bothContract, &eqDiffs)
		if len(eqDiffs) != 0 {
			t.Fatalf("same-order errors.Join(sameA, sameB) unexpectedly differed:\n%s", formatR6Diffs(eqDiffs))
		}
		// Swapped order of same-text distinct sentinels -> MUST report err_sentinel_pos!
		var swapDiffs []r6Diff
		compareR6ErrorsWithContract("err", errors.Join(sameA, sameB), errors.Join(sameB, sameA), bothContract, &swapDiffs)
		foundPosDiff := false
		for _, d := range swapDiffs {
			if d.Category == "err_sentinel_pos" {
				foundPosDiff = true
				break
			}
		}
		if !foundPosDiff {
			t.Fatalf("expected err_sentinel_pos for swapped same-text sentinels in Join(a,b) vs Join(b,a), got: %s", formatR6Diffs(swapDiffs))
		}
		// Wrapped Join with freshly allocated outer wrappers: same order passes, swapped order fails!
		wSame1 := fmt.Errorf("outer: %w", errors.Join(sameA, sameB))
		wSame2 := fmt.Errorf("outer: %w", errors.Join(sameA, sameB))
		wSwap := fmt.Errorf("outer: %w", errors.Join(sameB, sameA))
		var wrapEqDiffs []r6Diff
		compareR6ErrorsWithContract("err", wSame1, wSame2, bothContract, &wrapEqDiffs)
		if len(wrapEqDiffs) != 0 {
			t.Fatalf("fresh wrappers around identical Join order unexpectedly differed:\n%s", formatR6Diffs(wrapEqDiffs))
		}
		var wrapSwapDiffs []r6Diff
		compareR6ErrorsWithContract("err", wSame1, wSwap, bothContract, &wrapSwapDiffs)
		if len(wrapSwapDiffs) == 0 {
			t.Fatal("failed to detect swapped same-text sentinels inside wrapped errors.Join")
		}
	})

	// 6. Custom Is(error) bool behavior difference MUST fail.
	t.Run("N1_CustomIsBehaviorDetected", func(t *testing.T) {
		e1 := &n1CustomIsError{Msg: "custom", Target: sentinelA}
		e2 := &n1CustomIsError{Msg: "custom", Target: sentinelB}
		var diffs []r6Diff
		compareR6ErrorsWithContract("err", e1, e2, contractA, &diffs)
		if len(diffs) == 0 {
			t.Fatal("failed to detect custom Is() mismatch")
		}
	})

	// 7. Uncomparable custom error type (value struct containing []string) MUST NOT panic!
	t.Run("N1_UncomparableErrorNoPanic", func(t *testing.T) {
		u1 := n1UncomparableError{Code: 7, Tags: []string{"a", "b"}}
		u2 := n1UncomparableError{Code: 7, Tags: []string{"a", "b"}}
		u3 := n1UncomparableError{Code: 7, Tags: []string{"a", "c"}}
		var eqDiffs []r6Diff
		compareR6ErrorsWithContract("err", u1, u2, nil, &eqDiffs)
		if len(eqDiffs) != 0 {
			t.Fatalf("equal uncomparable custom errors reported diffs: %v", eqDiffs)
		}
		var neqDiffs []r6Diff
		compareR6ErrorsWithContract("err", u1, u3, nil, &neqDiffs)
		if len(neqDiffs) == 0 {
			t.Fatal("failed to detect slice field difference in uncomparable custom error")
		}
	})

	// 8. Cyclic unwrap graph MUST terminate cleanly before stdlib errors.Is!
	t.Run("N1_CyclicUnwrapGraphBoundedTermination", func(t *testing.T) {
		c1 := &n1CyclicError{}
		c2 := &n1CyclicError{Next: c1}
		c1.Next = c2
		var diffs []r6Diff
		compareR6ErrorsWithContract("err", c1, c1, contractA, &diffs)
		foundCycle := false
		for _, d := range diffs {
			if d.Category == "err_graph_cycle" {
				foundCycle = true
			}
		}
		if !foundCycle {
			t.Fatalf("expected err_graph_cycle on cyclic unwrap graph, got %v", diffs)
		}
	})

	// 9. Live 5-route parity where EVERY callback invocation allocates a FRESH 2-layer wrapper
	// around sentinelA (no shared wrapper pointer!) across BOTH error orders.
	initFreshWrapCB := func(b *r6StateBundle[r6IdxDynamicStruct]) {
		b.dst.Any = &t6CallbackProbe{
			FreshWrapSentinel: sentinelA,
			FreshWrapCode:     409,
			FreshWrapField:    "any",
			Log:               &b.logs,
		}
	}

	// Order A: callback error BEFORE int field type mismatch -> fresh 2-layer wrapper wins; subsequent fields still decoded!
	orderA := `{"any":{"v":1},"id":"bad_int","name":"after_cb_and_mismatch","sibling":77}`
	wA, outcomesA, _ := checkIndexVsTapeFiveWayWithContract[r6IdxDynamicStruct](t, orderA, diffStdOpts, false, initFreshWrapCB, contractA)
	if wA.IndexBinderEntered != 1 || wA.IndexSubtreeFallback != 1 {
		t.Fatalf("T6 Order A expected index+subtree, got %+v", wA)
	}
	for _, oc := range outcomesA {
		var gotCBErr *t6CustomCBError
		if !errors.As(oc.err, &gotCBErr) || !errors.Is(oc.err, sentinelA) || errors.Is(oc.err, sentinelB) || gotCBErr.Code != 409 || gotCBErr.Field != "any" {
			t.Fatalf("T6 Order A (%s) lost typed callback error / sentinel identity: %T (%#v)", oc.name, oc.err, oc.err)
		}
		if oc.bundle.dst.Name != "after_cb_and_mismatch" || oc.bundle.dst.Sibling != 77 || len(oc.bundle.logs) != 1 {
			t.Fatalf("T6 Order A (%s) violated OptDec continue semantics: dst=%+v logs=%v", oc.name, oc.bundle.dst, oc.bundle.logs)
		}
	}

	// Order B: int field type mismatch BEFORE callback error -> *MismatchTypeError wins; callback & subsequent fields still run!
	orderB := `{"id":"bad_int","any":{"v":2},"name":"after_mismatch_and_cb","sibling":88}`
	wB, outcomesB, _ := checkIndexVsTapeFiveWayWithContract[r6IdxDynamicStruct](t, orderB, diffStdOpts, false, initFreshWrapCB, nil)
	if wB.IndexBinderEntered != 1 || wB.IndexSubtreeFallback != 1 {
		t.Fatalf("T6 Order B expected index+subtree, got %+v", wB)
	}
	for _, oc := range outcomesB {
		var mErr *MismatchTypeError
		if !errors.As(oc.err, &mErr) {
			t.Fatalf("T6 Order B (%s) expected *MismatchTypeError to win first-error order, got %T (%v)", oc.name, oc.err, oc.err)
		}
		if oc.bundle.dst.Name != "after_mismatch_and_cb" || oc.bundle.dst.Sibling != 88 || len(oc.bundle.logs) != 1 {
			t.Fatalf("T6 Order B (%s) violated OptDec continue semantics: dst=%+v logs=%v", oc.name, oc.bundle.dst, oc.bundle.logs)
		}
	}
}

// TestT7_ErrorAndUTF8RepairCursorAndOwnershipParity verifies T7 & N2 (§3.3 / §10):
//   - Plain valid UTF-8 (no escape, no repair) CopyString vs zero-copy borrow on immutable DecodeOne(string)
//   - DecodeOne coordinate domain (original s): exact endPos, err.Pos, err.Src across valid, syntax, mismatch UTF-8-repaired inputs
//   - DecodeWithSession coordinate domain (repaired *s): exact *i, updated *s, err.Pos/Src, and multi-value continuation
//     (Public Unmarshal([]byte) & StreamDecoder caller-buffer mutation ownership is tested separately in root n2_public_ownership_test.go.)
func TestT7_ErrorAndUTF8RepairCursorAndOwnershipParity(t *testing.T) {
	strInRange := func(sub, base string) bool {
		if len(sub) == 0 || len(base) == 0 {
			return false
		}
		p := uintptr(unsafe.Pointer(unsafe.StringData(sub)))
		lo := uintptr(unsafe.Pointer(unsafe.StringData(base)))
		return p >= lo && p < lo+uintptr(len(base))
	}

	// 0. Plain valid UTF-8 (NO escape, NO UTF-8 repair) CopyString vs zero-copy borrow on immutable string input (§3.3).
	t.Run("PlainValidUTF8_CopyStringVsBorrow", func(t *testing.T) {
		plainSrc := `{"name":"plain_ascii_token_no_escape","id":7,"f64":-0.0}`
		for _, rep := range []decodeRepChoice{repForceIndex, repForceTape} {
			var dBorrow, dCopy r6IdxStruct
			_, endB, errB := decodeOneWithControl(plainSrc, 0, &dBorrow, &decodeTestControl{forceRep: rep})
			if errB != nil || endB != len(plainSrc) || dBorrow.Name != "plain_ascii_token_no_escape" {
				t.Fatalf("rep=%d zero-copy decode failed: err=%v end=%d name=%q", rep, errB, endB, dBorrow.Name)
			}
			if !strInRange(dBorrow.Name, plainSrc) {
				t.Fatalf("rep=%d CopyString=false expected zero-copy borrow of immutable string", rep)
			}
			_, endC, errC := decodeOneWithControl(plainSrc, 1<<_F_copy_string, &dCopy, &decodeTestControl{forceRep: rep})
			if errC != nil || endC != len(plainSrc) || dCopy.Name != "plain_ascii_token_no_escape" {
				t.Fatalf("rep=%d CopyString decode failed: err=%v end=%d name=%q", rep, errC, endC, dCopy.Name)
			}
			if strInRange(dCopy.Name, plainSrc) {
				t.Fatalf("rep=%d CopyString=true MUST detach plain valid UTF-8 string from input source", rep)
			}
		}
	})

	// 1. DecodeOne domain (original input coordinates on UTF-8-repaired valid, syntax-error, and mismatch-error inputs):
	decodeOneCases := []struct {
		name      string
		src       string
		expectErr string // "nil", "syntax", "mismatch"
	}{
		{"ValidUTF8Repair", "{\"name\":\"a\xffb\",\"id\":7,\"f64\":-0.0}", "nil"},
		{"SyntaxErrAfterUTF8Repair", "{\"name\":\"a\xffb\",\"id\":7,\"bad\":}", "syntax"},
		{"MismatchErrAfterUTF8Repair", "{\"name\":\"a\xffb\",\"id\":\"not_int\",\"f64\":-0.0}", "mismatch"},
	}
	for _, tc := range decodeOneCases {
		t.Run("DecodeOne_"+tc.name, func(t *testing.T) {
			w, outcomes, _ := checkIndexVsTapeFiveWayWithContract[r6IdxStruct](t, tc.src, diffStdOpts, false, r6IdxStructInits()[1], nil)
			if w.IndexScanStarted == 0 {
				t.Fatalf("expected index scan start, got %+v", w)
			}
			for _, oc := range outcomes {
				if classifyContractErr(oc.err) != tc.expectErr {
					t.Fatalf("route %s err kind got %q want %q (%v)", oc.name, classifyContractErr(oc.err), tc.expectErr, oc.err)
				}
				if tc.expectErr == "nil" {
					if oc.endPos != len(tc.src) || oc.bundle.dst.Name != "a\ufffdb" {
						t.Fatalf("route %s unexpected valid repair result: endPos=%d/%d name=%q", oc.name, oc.endPos, len(tc.src), oc.bundle.dst.Name)
					}
				} else {
					errSrc, ok := contractErrorSrc(oc.err)
					if !ok || errSrc != tc.src {
						t.Fatalf("route %s error source did not preserve original coordinate domain: ok=%v src=%q want=%q", oc.name, ok, errSrc, tc.src)
					}
				}
			}
		})
	}

	// 2. DecodeWithSession domain (repaired *s coordinates & multi-value continuation):
	t.Run("DecodeWithSession_UTF8RepairMultiValueAndMismatch", func(t *testing.T) {
		val1 := "{\"name\":\"a\xffb\",\"id\":1}"
		val2 := "{\"name\":\"second\",\"id\":2}"
		rawStream := val1 + " " + val2
		wantRepaired := "{\"name\":\"a\ufffdb\",\"id\":1} {\"name\":\"second\",\"id\":2}"
		wantPosAfter1 := len("{\"name\":\"a\ufffdb\",\"id\":1}")

		type sessRoute struct {
			name        string
			rep         decodeRepChoice
			forceScalar bool
		}
		routes := []sessRoute{
			{"auto", repAuto, false},
			{"idx_native", repForceIndex, false},
			{"idx_scalar", repForceIndex, true},
			{"tape_native", repForceTape, false},
			{"tape_scalar", repForceTape, true},
		}
		for _, r := range routes {
			s := rawStream
			pos := 0
			var sess *InputSession
			var d1, d2 r6IdxStruct
			var w1, w2 routeWitness
			err1 := decodeSessionWithControl(&s, &pos, diffStdOpts, &d1, &sess, &decodeTestControl{
				forceRep:    r.rep,
				forceScalar: r.forceScalar,
				witness:     &w1,
			})
			if err1 != nil || s != wantRepaired || pos != wantPosAfter1 || d1.Name != "a\ufffdb" || d1.ID != 1 {
				t.Fatalf("route %s step 1 failed: err=%v pos=%d(want %d) s=%q d1=%+v",
					r.name, err1, pos, wantPosAfter1, s, d1)
			}
			err2 := decodeSessionWithControl(&s, &pos, diffStdOpts, &d2, &sess, &decodeTestControl{
				forceRep:    r.rep,
				forceScalar: r.forceScalar,
				witness:     &w2,
			})
			if err2 != nil || pos != len(wantRepaired) || d2.Name != "second" || d2.ID != 2 {
				t.Fatalf("route %s step 2 failed: err=%v pos=%d/%d d2=%+v",
					r.name, err2, pos, len(wantRepaired), d2)
			}
		}

		// Session error domain: UTF-8 repair + type mismatch -> exact identical *i, *s, err.Pos, err.Src across all 5 routes!
		badStream := "{\"name\":\"a\xffb\",\"id\":\"bad\"} {\"id\":2}"
		var refPos int
		var refS string
		var refErr error
		for i, r := range routes {
			s := badStream
			pos := 0
			var d r6IdxStruct
			err := decodeSessionWithControl(&s, &pos, diffStdOpts, &d, nil, &decodeTestControl{
				forceRep:    r.rep,
				forceScalar: r.forceScalar,
			})
			if i == 0 {
				refPos, refS, refErr = pos, s, err
				if classifyContractErr(err) != "mismatch" {
					t.Fatalf("expected mismatch error on badStream, got %T (%v)", err, err)
				}
				continue
			}
			var ediffs []r6Diff
			compareR6Errors(r.name, refErr, err, &ediffs)
			if len(ediffs) > 0 || pos != refPos || s != refS {
				t.Fatalf("route %s session repair-mismatch divergence: pos=%d/%d sMatch=%v diffs:\n%s",
					r.name, pos, refPos, s == refS, formatR6Diffs(ediffs))
			}
		}
	})
}

func FuzzValidatedIndexDifferential(f *testing.F) {
	if err := ident.VerifyFromEnv(); err != nil {
		f.Fatalf("fuzz coordinator backend identity mismatch: %v", err)
	}
	requireWitnessIfEnvSet(f)
	verifyR6WorkerWitnessOrFatal(f)
	resetR6FuzzStats()

	var workerOnce sync.Once
	f.Cleanup(func() {
		execs := r6FuzzExecs.Load()
		scans := r6FuzzIndexScans.Load()
		binds := r6FuzzIndexBinds.Load()
		subtrees := r6FuzzSubtreeHits.Load()
		synBlocks := r6FuzzSyntaxBlocks.Load()
		if os.Getenv("SONIC_FUZZ_STATS") == "1" {
			fmt.Fprintf(os.Stderr, "[r6-index-fuzz pid=%d] execs=%d indexScans=%d indexBinds=%d subtreeHits=%d syntaxBlocks=%d\n",
				os.Getpid(), execs, scans, binds, subtrees, synBlocks)
		}
		if wdir := os.Getenv("SONIC_R6_WITNESS_DIR"); wdir != "" && execs > 0 {
			cur := ident.Current()
			familyMap := make(map[string]uint64)
			for p := jsonscan.LexProducer(1); p < 16; p++ {
				if cnt := r6FuzzProducerHits[p].Load(); cnt > 0 {
					familyMap[p.String()] = cnt
				}
			}
			globalFamily := jsonscan.LexProducerDisabled.String()
			forcedFamily := jsonscan.LexProducerDisabled.String()
			if jsonscan.WitnessEnabled {
				globalFamily = expectedDispatchedFamilyFromIdent(false).String()
				forcedFamily = expectedDispatchedFamilyFromIdent(true).String()
			}
			rec := map[string]any{
				"pid":                       os.Getpid(),
				"fuzz_fn":                   "FuzzValidatedIndexDifferential",
				"target_family":             "I1-I6_ValidatedIndex",
				"witness_enabled":           jsonscan.WitnessEnabled,
				"instrumentation":           cur.Instrumentation,
				"global_dispatched_family":  globalFamily,
				"forced_scalar_family":      forcedFamily,
				"observed_families":         familyMap,
				"route_dispatch_ratio_note": "3 non-forced routes (auto, idx_native, tape_native) vs 2 forced-Go-scalar routes (idx_scalar, tape_scalar) per 5-route check; counts are outer dispatched family occurrences, not asm/vector instruction ratios",
				"target_family_hits": map[string]uint64{
					"I1_struct":        r6FuzzFamilyI1.Load(),
					"I2_slice":         r6FuzzFamilyI2.Load(),
					"I3_scalar_slice":  r6FuzzFamilyI3.Load(),
					"I4_I6_dyn_struct": r6FuzzFamilyI4I6.Load(),
					"NoValidateJSON":   r6FuzzFamilyNoVal.Load(),
				},
				"execs":                 execs,
				"index_scans":           scans,
				"index_binds":           binds,
				"subtree_hits":          subtrees,
				"syntax_blocks":         synBlocks,
				"noval_invalid_skipped": r6FuzzNoValInvalidSkipped.Load(),
			}
			if b, err := json.Marshal(rec); err == nil {
				tmpPath := filepath.Join(wdir, fmt.Sprintf(".worker_%d.tmp", os.Getpid()))
				finalPath := filepath.Join(wdir, fmt.Sprintf("worker_%d.json", os.Getpid()))
				if os.WriteFile(tmpPath, b, 0o644) == nil {
					_ = os.Rename(tmpPath, finalPath)
				}
			}
		}
		if execs > 0 && (scans == 0 || binds == 0) {
			f.Fatalf("FuzzValidatedIndexDifferential fail-closed: zero index hits (scans=%d binds=%d)", scans, binds)
		}
	})

	for idx, s := range diffSeedCorpus() {
		f.Add([]byte(s), uint8(idx))
	}
	extraSeeds := []string{
		`{"id":1,"f32":-0.0,"f64":-0.0,"any":{"v":9},"anys":[{"v":1},[1e400],"ok"],"sibling":3}`,
		`[{"id":1,"name":"a"},{"id":2,"f64":-0.0}]`,
		`[1,2,-3,0,999999]`,
		`[-0.0,1.5,3.4028234663852886e+38]`,
		`["plain","esc\u0041\n","\uD800"]`,
	}
	for idx, s := range extraSeeds {
		f.Add([]byte(s), uint8(100+idx))
	}

	sInits := r6IdxStructInits()
	dInits := r6DynamicStructInits()

	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		workerOnce.Do(func() {
			if err := ident.VerifyFromEnv(); err != nil {
				t.Fatalf("fuzz worker backend identity mismatch: %v", err)
			}
			verifyR6WorkerWitnessOrFatal(t)
		})
		if len(data) > 64<<10 {
			return
		}
		s := string(data)
		switch mode % 6 {
		case 0:
			r6FuzzFamilyI1.Add(1)
			initFn := sInits[int(mode>>3)%len(sInits)]
			checkIndexVsTapeFourWay[r6IdxStruct](t, s, diffStdOpts, true, initFn)
		case 1:
			r6FuzzFamilyI4I6.Add(1)
			initFn := dInits[int(mode>>3)%len(dInits)]
			checkIndexVsTapeFourWay[r6IdxDynamicStruct](t, s, diffStdOpts, true, initFn)
		case 2:
			r6FuzzFamilyI2.Add(1)
			checkIndexVsTapeFourWay[[]r6IdxStruct](t, s, diffStdOpts, true, nil)
			checkIndexVsTapeFourWay[[]*r6IdxStruct](t, s, diffStrictURCOpts, false, nil)
		case 3:
			r6FuzzFamilyI3.Add(1)
			checkIndexVsTapeFourWay[[]int](t, s, diffStdOpts, true, nil)
			checkIndexVsTapeFourWay[[]float32](t, s, diffStdOpts, true, nil)
			checkIndexVsTapeFourWay[[]string](t, s, diffFastCopyOpts, false, nil)
		case 4:
			r6FuzzFamilyI4I6.Add(1)
			r6FuzzFamilyI1.Add(1)
			checkIndexVsTapeFourWay[r6IdxDynamicStruct](t, s, diffStrictURCOpts, false, dInits[1])
			checkIndexVsTapeFourWay[r6IdxStruct](t, s, diffFastCopyOpts, false, sInits[2])
		case 5:
			if json.Valid(data) && !strings.Contains(s, "\x00") {
				r6FuzzFamilyNoVal.Add(1)
				checkIndexVsTapeFourWay[r6IdxStruct](t, s, diffNoValValidOpts, false, nil)
				checkIndexVsTapeFourWay[r6IdxDynamicStruct](t, s, diffNoValValidOpts, false, dInits[1])
			} else {
				r6FuzzFamilyI3.Add(1)
				checkIndexVsTapeFourWay[[]float64](t, s, diffStdOpts, true, nil)
			}
		}
	})
}
