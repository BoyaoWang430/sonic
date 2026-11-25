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

package encoder

import (
	"reflect"
	"sync"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/resolver"
)

// encodedField augments shared resolver.FieldMeta with encoder-only pre-quoted
// JSON key spans and a 32-byte trailing zero-padded byte owner.
type encodedField struct {
	meta             *resolver.FieldMeta
	owner            []byte
	keyOffset        int
	commaKeyLen      int
	hasStructBegin   bool
	paddedCommaKey   string // logical "," + Quote(Name) + ":"
	keyNeedsSanitize bool
}

func (f *encodedField) commaKeyPtr() *byte {
	return &f.owner[f.keyOffset]
}

var (
	keyPlanLock  sync.RWMutex
	keyPlanCache = map[reflect.Type][]encodedField{}
)

// resolveEncodedStruct builds or retrieves encoder-only pre-quoted key plans
// for struct type vt without polluting shared resolver metadata.
func resolveEncodedStruct(vt reflect.Type) []encodedField {
	keyPlanLock.RLock()
	cached, ok := keyPlanCache[vt]
	keyPlanLock.RUnlock()
	if ok {
		return cached
	}

	metas := resolver.ResolveStruct(vt)
	built := buildEncodedFields(metas)

	keyPlanLock.Lock()
	if existing, ok := keyPlanCache[vt]; ok {
		keyPlanLock.Unlock()
		return existing
	}
	keyPlanCache[vt] = built
	keyPlanLock.Unlock()
	return built
}

func buildEncodedFields(metas []resolver.FieldMeta) []encodedField {
	if len(metas) == 0 {
		return nil
	}
	out := make([]encodedField, len(metas))
	est := 32
	for i := range metas {
		est += len(metas[i].Name) + 8
	}
	blob := make([]byte, 0, est)

	type keySpan struct {
		start      int
		commaLen   int
		hasStructB bool
		sanitize   bool
	}
	var stackSpans [32]keySpan
	var spans []keySpan
	if len(metas) <= len(stackSpans) {
		spans = stackSpans[:len(metas)]
	} else {
		spans = make([]keySpan, len(metas))
	}

	for i := range metas {
		fm := &metas[i]
		sanitize := alg.NeedsSanitize(fm.Name)
		start := len(blob)
		blob = append(blob, ',')
		blob = alg.Quote(blob, fm.Name, false)
		blob = append(blob, ':')
		commaKeyLen := len(blob) - start
		hasStructB := false
		if fm.Type.Kind() == reflect.Struct && !sanitize && commaKeyLen <= 254 {
			blob = append(blob, '{')
			hasStructB = true
		}
		spans[i] = keySpan{
			start:      start,
			commaLen:   commaKeyLen,
			hasStructB: hasStructB,
			sanitize:   sanitize,
		}
	}

	logicalLen := len(blob)
	var z32 [32]byte
	blob = append(blob, z32[:]...)
	blobStr := unsafe.String(unsafe.SliceData(blob), logicalLen)

	for i := range metas {
		sp := spans[i]
		out[i] = encodedField{
			meta:             &metas[i],
			owner:            blob,
			keyOffset:        sp.start,
			commaKeyLen:      sp.commaLen,
			hasStructBegin:   sp.hasStructB,
			paddedCommaKey:   blobStr[sp.start : sp.start+sp.commaLen],
			keyNeedsSanitize: sp.sanitize,
		}
	}
	return out
}
