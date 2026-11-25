/*
 * Copyright 2024 ByteDance Inc.
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

package ast

import (
	"errors"
	"math"
	"sort"
	"strconv"

	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// ErrPatchConflict is returned by PatchByPath when two patches target the same
// path or ancestor/descendant overlapping spans.
var ErrPatchConflict = errors.New("patch paths overlap or duplicate")

// ErrInvalidPatchRaw is returned when a PathPatch with Raw=true carries invalid JSON.
var ErrInvalidPatchRaw = errors.New("invalid raw json value in patch")

// PathPatch specifies replacing an existing JSON value at Path with Value (or
// RawValue when Raw is true).
type PathPatch struct {
	Path     []interface{}
	Value    interface{}
	RawValue []byte
	Raw      bool
}

type resolvedPatch struct {
	start  int
	end    int
	repOff int
	repLen int
	rawRef []byte
	isRaw  bool
}

// PatchByPath locates all existing, non-overlapping target paths on a single
// source JSON slice, encodes replacement values, and splices a new JSON byte
// slice in one allocation while preserving unmodified bytes verbatim.
func PatchByPath(src []byte, patches ...PathPatch) ([]byte, error) {
	return PatchByPathString(rt.Mem2Str(src), patches...)
}

func validateFullRootJSON(src string) error {
	p := NewParserObj(src)
	_, e := p.skip()
	if e != 0 {
		return p.syntaxError(e)
	}
	if skipBlank(p.s, p.p) != -int(types.ERR_EOF) {
		return p.syntaxError(types.ERR_INVALID_CHAR)
	}
	return nil
}

// PatchByPathString is like PatchByPath for a string input.
func PatchByPathString(src string, patches ...PathPatch) ([]byte, error) {
	if len(patches) == 0 {
		if err := validateFullRootJSON(src); err != nil {
			return nil, err
		}
		out := make([]byte, len(src))
		copy(out, src)
		return out, nil
	}

	paths := make([][]interface{}, len(patches))
	for i := range patches {
		paths[i] = patches[i].Path
	}
	plan, err := CompilePathPlan(paths...)
	if err != nil {
		return nil, err
	}
	return patchWithPlanString(src, plan, patches)
}

// PatchWithPlan applies replacement values corresponding to a precompiled PathPlan.
func PatchWithPlan(src []byte, plan *PathPlan, patches ...PathPatch) ([]byte, error) {
	if plan == nil || len(patches) != plan.NumPaths() {
		return nil, errors.New("patch count does not match PathPlan")
	}
	return patchWithPlanString(rt.Mem2Str(src), plan, patches)
}

func patchWithPlanString(src string, plan *PathPlan, patches []PathPatch) ([]byte, error) {
	if err := validateFullRootJSON(src); err != nil {
		return nil, err
	}
	n := len(patches)
	if n == 0 {
		out := make([]byte, len(src))
		copy(out, src)
		return out, nil
	}

	var stackSpans [8]locatedSpan
	var spans []locatedSpan
	if n <= len(stackSpans) {
		spans = stackSpans[:n]
	} else {
		spans = make([]locatedSpan, n)
	}

	view := inputView{src: src, base: 0}
	if err := locateWithPlan(view, plan, false, spans); err != nil {
		return nil, err
	}

	// Verify all targets exist before encoding or splicing.
	var stackResolved [8]resolvedPatch
	var resolved []resolvedPatch
	if n <= len(stackResolved) {
		resolved = stackResolved[:n]
	} else {
		resolved = make([]resolvedPatch, n)
	}

	for i := 0; i < n; i++ {
		if !spans[i].found || spans[i].start < 0 || spans[i].end < spans[i].start {
			return nil, ErrNotExist
		}
		if switchRawType(src[spans[i].start]) == _V_NONE {
			return nil, ErrNotExist
		}
		resolved[i] = resolvedPatch{
			start: spans[i].start,
			end:   spans[i].end,
		}
	}

	// Encode all replacement values before sorting so errors reflect caller order
	// and happen prior to final output allocation.
	var stackEnc [256]byte
	encScratch := stackEnc[:0]
	var delta int64
	for i := 0; i < n; i++ {
		origLen := int64(resolved[i].end - resolved[i].start)
		if patches[i].Raw {
			if !validJSONBytes(patches[i].RawValue) {
				return nil, ErrInvalidPatchRaw
			}
			resolved[i].isRaw = true
			resolved[i].rawRef = patches[i].RawValue
			delta += int64(len(patches[i].RawValue)) - origLen
			continue
		}
		off := len(encScratch)
		if !appendScalarPatchValue(&encScratch, patches[i].Value) {
			if err := encodePatchValue(&encScratch, patches[i].Value); err != nil {
				return nil, err
			}
		}
		repLen := len(encScratch) - off
		resolved[i].repOff = off
		resolved[i].repLen = repLen
		delta += int64(repLen) - origLen
	}

	// Sort spans by (start ASC, end DESC) and reject duplicate/overlapping spans.
	if n <= 16 {
		for i := 1; i < n; i++ {
			cur := resolved[i]
			j := i - 1
			for j >= 0 && (resolved[j].start > cur.start || (resolved[j].start == cur.start && resolved[j].end < cur.end)) {
				resolved[j+1] = resolved[j]
				j--
			}
			resolved[j+1] = cur
		}
	} else {
		sort.Slice(resolved, func(i, j int) bool {
			if resolved[i].start != resolved[j].start {
				return resolved[i].start < resolved[j].start
			}
			return resolved[i].end > resolved[j].end
		})
	}

	for i := 1; i < n; i++ {
		if resolved[i].start < resolved[i-1].end {
			return nil, ErrPatchConflict
		}
		// Guard empty-span equality edge case.
		if resolved[i].start == resolved[i-1].start {
			return nil, ErrPatchConflict
		}
	}

	// Single exact-sized allocation and splice with overflow check.
	outLen64 := int64(len(src)) + delta
	if outLen64 < 0 || outLen64 > int64(math.MaxInt) {
		return nil, errors.New("patched output length overflow")
	}
	out := make([]byte, 0, int(outLen64))
	cursor := 0
	for i := 0; i < n; i++ {
		rp := &resolved[i]
		out = append(out, src[cursor:rp.start]...)
		if rp.isRaw {
			out = append(out, rp.rawRef...)
		} else {
			out = append(out, encScratch[rp.repOff:rp.repOff+rp.repLen]...)
		}
		cursor = rp.end
	}
	out = append(out, src[cursor:]...)
	return out, nil
}

func appendScalarPatchValue(buf *[]byte, val interface{}) bool {
	switch v := val.(type) {
	case nil:
		*buf = append(*buf, "null"...)
		return true
	case bool:
		if v {
			*buf = append(*buf, "true"...)
		} else {
			*buf = append(*buf, "false"...)
		}
		return true
	case string:
		quote(buf, v)
		return true
	case int:
		*buf = strconv.AppendInt(*buf, int64(v), 10)
		return true
	case int64:
		*buf = strconv.AppendInt(*buf, v, 10)
		return true
	case int32:
		*buf = strconv.AppendInt(*buf, int64(v), 10)
		return true
	case uint:
		*buf = strconv.AppendUint(*buf, uint64(v), 10)
		return true
	case uint64:
		*buf = strconv.AppendUint(*buf, v, 10)
		return true
	case uint32:
		*buf = strconv.AppendUint(*buf, uint64(v), 10)
		return true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
		*buf = number.AppendFloat64(*buf, v)
		return true
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return false
		}
		*buf = number.AppendFloat32(*buf, v)
		return true
	default:
		return false
	}
}
