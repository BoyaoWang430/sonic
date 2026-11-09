/*
 * Copyright 2021 ByteDance Inc.
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

package resolver

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	_ "unsafe"

	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/rt"
)

type FieldOpts int
type OffsetType int

const (
	F_omitempty FieldOpts = 1 << iota
	F_stringize
	F_omitzero
)

const (
	F_offset OffsetType = iota
	F_deref
)

type Offset struct {
	Size uintptr
	Kind OffsetType
	Type reflect.Type
}

type FieldMeta struct {
	Name                 string
	PaddedCommaKey       string // "," + Quote(Name) + ":" backed by +32 zero-padded bytes
	PaddedStructBeginKey string // "," + Quote(Name) + ":{" backed by +32 zero-padded bytes (when <= 32B)
	Path                 []Offset
	Opts                 FieldOpts
	Type                 reflect.Type
	IsZero               func(reflect.Value) bool
	KeyNeedsSanitize     bool
}

func (self *FieldMeta) String() string {
	var path []string
	var opts []string

	/* dump the field path */
	for _, off := range self.Path {
		if off.Kind == F_offset {
			path = append(path, fmt.Sprintf("%d", off.Size))
		} else {
			path = append(path, fmt.Sprintf("%d.(*%s)", off.Size, off.Type))
		}
	}

	/* check for "string" */
	if (self.Opts & F_stringize) != 0 {
		opts = append(opts, "string")
	}

	/* check for "omitempty" */
	if (self.Opts & F_omitempty) != 0 {
		opts = append(opts, "omitempty")
	}

	/* format the field */
	return fmt.Sprintf(
		"{Field \"%s\" @ %s, opts=%s, type=%s}",
		self.Name,
		strings.Join(path, "."),
		strings.Join(opts, ","),
		self.Type,
	)
}

func (self *FieldMeta) optimize() {
	var n int
	var v uintptr

	/* merge adjacent offsets */
	for _, o := range self.Path {
		if v += o.Size; o.Kind == F_deref {
			self.Path[n].Size = v
			self.Path[n].Type, v = o.Type, 0
			self.Path[n].Kind, n = F_deref, n+1
		}
	}

	/* last offset value */
	if v != 0 {
		self.Path[n].Size = v
		self.Path[n].Type = nil
		self.Path[n].Kind = F_offset
		n++
	}

	/* must be at least 1 offset */
	if n != 0 {
		self.Path = self.Path[:n]
	} else {
		self.Path = []Offset{{Kind: F_offset}}
	}
}

func resolveFields(vt reflect.Type) []FieldMeta {
	tfv := typeFields(vt)
	nFields := len(tfv.list)
	if nFields == 0 {
		return nil
	}
	ret := make([]FieldMeta, nFields)
	totalDepth := 0
	for i := range tfv.list {
		totalDepth += len(tfv.list[i].index)
	}
	offsetSlab := make([]Offset, totalDepth)
	slabPos := 0

	/* convert each field */
	for fIdx, fv := range tfv.list {
		fm := &ret[fIdx]

		item := vt
		depth := len(fv.index)
		path := offsetSlab[slabPos : slabPos+depth : slabPos+depth]
		slabPos += depth

		/* check for "string" */
		if fv.quoted {
			fm.Opts |= F_stringize
		}

		/* check for "omitempty" */
		if fv.omitEmpty {
			fm.Opts |= F_omitempty
		}

		/* handle the "omitzero" */
		handleOmitZero(fv, fm)

		/* dump the field path */
		for d, i := range fv.index {
			kind := F_offset
			fval := item.Field(i)
			item = fval.Type

			/* deref the pointer if needed */
			if item.Kind() == reflect.Ptr {
				kind = F_deref
				item = item.Elem()
			}

			/* add to path */
			path[d] = Offset{
				Kind: kind,
				Type: item,
				Size: fval.Offset,
			}
		}

		/* get the index to the last offset */
		idx := len(path) - 1
		fvt := path[idx].Type

		/* do not dereference into fields */
		if path[idx].Kind == F_deref {
			fvt = reflect.PtrTo(fvt)
			path[idx].Kind = F_offset
		}

		fm.Type = fvt
		fm.Path = path
		fm.Name = fv.name
		fm.KeyNeedsSanitize = alg.NeedsSanitize(fm.Name)
	}

	populateKeyBlob(ret)

	/* optimize the offsets */
	for i := range ret {
		ret[i].optimize()
	}

	/* all done */
	return ret
}

var (
	fieldLock  = sync.RWMutex{}
	fieldCache = map[reflect.Type][]FieldMeta{}
)

func ResetFieldCacheForTest() {
	fieldLock.Lock()
	fieldCache = map[reflect.Type][]FieldMeta{}
	fieldLock.Unlock()
}

func ResolveStruct(vt reflect.Type) []FieldMeta {
	var ok bool
	var fm []FieldMeta

	/* attempt to read from cache */
	fieldLock.RLock()
	fm, ok = fieldCache[vt]
	fieldLock.RUnlock()

	/* check if it was cached */
	if ok {
		return fm
	}

	/* otherwise use write-lock */
	fieldLock.Lock()
	defer fieldLock.Unlock()

	/* double check */
	if fm, ok = fieldCache[vt]; ok {
		return fm
	}

	/* resolve the field */
	fm = resolveFields(vt)
	fieldCache[vt] = fm
	return fm
}

func handleOmitZero(fv StdField, fm *FieldMeta) {
	if fv.omitZero {
		fm.Opts |= F_omitzero
		fm.IsZero = fv.isZero
	}
}

// populateKeyBlob packs all fields' quoted JSON keys (,"name": and ,"name":{)
// into a single per-struct byte slice with a single trailing 32-byte zero
// safety zone so 16/32-byte unaligned loads in appendPaddedKey never cross an
// unmapped page boundary.
func populateKeyBlob(ret []FieldMeta) {
	if len(ret) == 0 {
		return
	}
	est := 32
	for i := range ret {
		est += len(ret[i].Name) + 8
	}
	blob := make([]byte, 0, est)
	type keySpan struct {
		start      int
		commaLen   int
		hasStructB bool
	}
	var stackSpans [32]keySpan
	var spans []keySpan
	if len(ret) <= len(stackSpans) {
		spans = stackSpans[:len(ret)]
	} else {
		spans = make([]keySpan, len(ret))
	}
	for i := range ret {
		fm := &ret[i]
		start := len(blob)
		blob = append(blob, ',')
		blob = alg.Quote(blob, fm.Name, false)
		blob = append(blob, ':')
		commaKeyLen := len(blob) - start
		hasStructB := false
		if fm.Type.Kind() == reflect.Struct && !fm.KeyNeedsSanitize && commaKeyLen <= 31 {
			blob = append(blob, '{')
			hasStructB = true
		}
		spans[i] = keySpan{
			start:      start,
			commaLen:   commaKeyLen,
			hasStructB: hasStructB,
		}
	}
	var z32 [32]byte
	blob = append(blob, z32[:]...)
	blobStr := rt.Mem2Str(blob)
	for i := range ret {
		sp := spans[i]
		ret[i].PaddedCommaKey = blobStr[sp.start : sp.start+sp.commaLen]
		if sp.hasStructB {
			ret[i].PaddedStructBeginKey = blobStr[sp.start : sp.start+sp.commaLen+1]
		}
	}
}
