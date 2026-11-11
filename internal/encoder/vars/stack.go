/**
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

package vars

import (
	"bytes"
	"sync"
	"unsafe"

	"github.com/bytedance/sonic/internal/caching"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/option"
)

type State struct {
	x int
	f uint64
	p unsafe.Pointer
	q unsafe.Pointer
}

type MapKVEntry struct {
	K string
	V any
}

const (
	maxPooledMapKV = 4096
	inlineStackCap = 64
)

type Stack struct {
	sp        int
	ptrSp     int
	kvSp      int
	kvOrderSp int
	sb        [inlineStackCap]State
	sbExtra   []State
	ptrSlots  [64]unsafe.Pointer
	KVBuf     []MapKVEntry
	KVOrder   []int
	ValPtr    unsafe.Pointer
}

var (
	bytesPool = sync.Pool{}
	stackPool = sync.Pool{
		New: func() interface{} {
			return &Stack{}
		},
	}
	bufferPool     = sync.Pool{}
	programCache    = caching.CreateProgramCache()
	programCachePV  = caching.CreateProgramCache()
	rootEncCache    = caching.CreateProgramCache()
	cacheResetMu    sync.Mutex
	cacheResetHooks []func()
)

func RegisterCacheReset(fn func()) {
	cacheResetMu.Lock()
	cacheResetHooks = append(cacheResetHooks, fn)
	cacheResetMu.Unlock()
}

func ResetProgramCache() {
	programCache.Reset()
	programCachePV.Reset()
	rootEncCache.Reset()
	cacheResetMu.Lock()
	hooks := append([]func(){}, cacheResetHooks...)
	cacheResetMu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}

func NewBytes() *[]byte {
	if ret := bytesPool.Get(); ret != nil {
		return ret.(*[]byte)
	} else {
		ret := make([]byte, 0, option.DefaultEncoderBufferSize)
		return &ret
	}
}

func NewStack() *Stack {
	ret := stackPool.Get().(*Stack)
	ret.sp = 0
	ret.ptrSp = 0
	ret.kvSp = 0
	ret.kvOrderSp = 0
	return ret
}

func ResetStack(p *Stack) {
	p.ValPtr = nil
	for i := 0; i < p.ptrSp; i++ {
		p.ptrSlots[i] = nil
	}
	p.ptrSp = 0
	if p.kvSp > 0 {
		for i := 0; i < p.kvSp; i++ {
			p.KVBuf[i] = MapKVEntry{}
		}
		p.KVBuf = p.KVBuf[:0]
		p.kvSp = 0
	}
	if p.kvOrderSp > 0 {
		p.KVOrder = p.KVOrder[:0]
		p.kvOrderSp = 0
	}
	if cap(p.KVBuf) > maxPooledMapKV {
		p.KVBuf = nil
	}
	if cap(p.KVOrder) > maxPooledMapKV {
		p.KVOrder = nil
	}
	limit := p.sp
	if limit > inlineStackCap {
		limit = inlineStackCap
	}
	for i := 0; i < limit; i++ {
		p.sb[i] = State{}
	}
	p.sbExtra = nil
	p.sp = 0
}

func (s *Stack) ReserveMapKV(n int) (baseKV int, baseOrd int) {
	baseKV = s.kvSp
	endKV := baseKV + n
	if cap(s.KVBuf) < endKV {
		newCap := cap(s.KVBuf) * 2
		if newCap < 64 {
			newCap = 64
		}
		if newCap < endKV {
			newCap = endKV
		}
		next := make([]MapKVEntry, endKV, newCap)
		copy(next, s.KVBuf[:baseKV])
		s.KVBuf = next
	} else {
		s.KVBuf = s.KVBuf[:endKV]
	}
	s.kvSp = endKV

	baseOrd = s.kvOrderSp
	endOrd := baseOrd + n
	if cap(s.KVOrder) < endOrd {
		newCap := cap(s.KVOrder) * 2
		if newCap < 64 {
			newCap = 64
		}
		if newCap < endOrd {
			newCap = endOrd
		}
		next := make([]int, endOrd, newCap)
		copy(next, s.KVOrder[:baseOrd])
		s.KVOrder = next
	} else {
		s.KVOrder = s.KVOrder[:endOrd]
	}
	s.kvOrderSp = endOrd
	return baseKV, baseOrd
}

func (s *Stack) GrowMapKVOne(k string, v any, localIdx int) {
	s.KVBuf = append(s.KVBuf, MapKVEntry{K: k, V: v})
	s.kvSp = len(s.KVBuf)
	s.KVOrder = append(s.KVOrder, localIdx)
	s.kvOrderSp = len(s.KVOrder)
}

func (s *Stack) TrimMapKV(baseKV int, baseOrd int, count int) ([]MapKVEntry, []int) {
	for j := baseKV + count; j < s.kvSp; j++ {
		s.KVBuf[j] = MapKVEntry{}
	}
	s.kvSp = baseKV + count
	s.KVBuf = s.KVBuf[:s.kvSp]
	s.kvOrderSp = baseOrd + count
	s.KVOrder = s.KVOrder[:s.kvOrderSp]
	return s.KVBuf[baseKV:s.kvSp], s.KVOrder[baseOrd:s.kvOrderSp]
}

func (s *Stack) ReleaseMapKV(baseKV int, count int, baseOrd int) {
	clearSlice := s.KVBuf[baseKV : baseKV+count]
	for i := range clearSlice {
		clearSlice[i] = MapKVEntry{}
	}
	s.KVBuf = s.KVBuf[:baseKV]
	s.kvSp = baseKV
	s.KVOrder = s.KVOrder[:baseOrd]
	s.kvOrderSp = baseOrd
}

func (s *Stack) PushPtr(ptr unsafe.Pointer) (unsafe.Pointer, int) {
	if s.ptrSp < len(s.ptrSlots) {
		idx := s.ptrSp
		s.ptrSp = idx + 1
		s.ptrSlots[idx] = ptr
		return unsafe.Pointer(&s.ptrSlots[idx]), idx
	}
	box := new(unsafe.Pointer)
	*box = ptr
	return unsafe.Pointer(box), -1
}

func (s *Stack) PopPtr(idx int) {
	if idx >= 0 {
		s.ptrSlots[idx] = nil
		s.ptrSp = idx
	}
}

func (s *Stack) extraAt(idx int) *State {
	if s.sbExtra == nil {
		s.sbExtra = make([]State, MaxStack-inlineStackCap)
	}
	return &s.sbExtra[idx-inlineStackCap]
}

func (s *Stack) Top() *State {
	if uint(s.sp) < inlineStackCap {
		return &s.sb[s.sp]
	}
	return s.extraAt(s.sp)
}

func (s *Stack) Cur() *State {
	idx := s.sp - 1
	if uint(idx) < inlineStackCap {
		return &s.sb[idx]
	}
	return s.extraAt(idx)
}

func (s *Stack) Push(v State) bool {
	if s.sp >= MaxStack {
		return false
	}
	if uint(s.sp) < inlineStackCap {
		s.sb[s.sp] = v
	} else {
		*s.extraAt(s.sp) = v
	}
	s.sp++
	return true
}

func (s *Stack) EnterDepth(n int) bool {
	if s.sp+n > MaxStack {
		return false
	}
	s.sp += n
	return true
}

func (s *Stack) LeaveDepth(n int) {
	s.sp -= n
}

func (s *Stack) Pop() State {
	s.sp--
	if uint(s.sp) < inlineStackCap {
		ret := s.sb[s.sp]
		s.sb[s.sp] = State{}
		return ret
	}
	slot := s.extraAt(s.sp)
	ret := *slot
	*slot = State{}
	return ret
}

func (s *Stack) Load() (int, uint64, unsafe.Pointer, unsafe.Pointer) {
	st := s.Cur()
	return st.x, st.f, st.p, st.q
}

func (s *Stack) Save(x int, f uint64, p unsafe.Pointer, q unsafe.Pointer) bool {
	return s.Push(State{x: x, f: f, p: p, q: q})
}

func (s *Stack) Drop() (int, uint64, unsafe.Pointer, unsafe.Pointer) {
	st := s.Pop()
	return st.x, st.f, st.p, st.q
}

func NewBuffer() *bytes.Buffer {
	if ret := bufferPool.Get(); ret != nil {
		return ret.(*bytes.Buffer)
	} else {
		return bytes.NewBuffer(make([]byte, 0, option.DefaultEncoderBufferSize))
	}
}

func FreeBytes(p *[]byte) {
	if rt.CanSizeResue(cap(*p)) {
		(*p) = (*p)[:0]
		bytesPool.Put(p)
	}
}

func FreeStack(p *Stack) {
	p.sp = 0
	p.ptrSp = 0
	p.ValPtr = nil
	p.sbExtra = nil
	if cap(p.KVBuf) > maxPooledMapKV {
		p.KVBuf = nil
	}
	if cap(p.KVOrder) > maxPooledMapKV {
		p.KVOrder = nil
	}
	stackPool.Put(p)
}

func FreeBuffer(p *bytes.Buffer) {
	if rt.CanSizeResue(cap(p.Bytes())) {
		p.Reset()
		bufferPool.Put(p)
	}
}

var (
	ArgPtrs   = []bool{true, true, true, false}
	LocalPtrs = []bool{}

	ArgPtrs_generic   = []bool{true}
	LocalPtrs_generic = []bool{}
)
