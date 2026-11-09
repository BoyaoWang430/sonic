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

package caching

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/bytedance/sonic/internal/rt"
)

/** Program Map **/

const (
	_LoadFactor   = 0.5
	_InitCapacity = 4096 // must be a power of 2
)

type _ProgramMap struct {
	n uint64
	m uint32
	b []_ProgramEntry
}

type _ProgramEntry struct {
	vt *rt.GoType
	fn interface{}
}

func newProgramMap() *_ProgramMap {
	return &_ProgramMap{
		n: 0,
		m: _InitCapacity - 1,
		b: make([]_ProgramEntry, _InitCapacity),
	}
}

func (self *_ProgramMap) copy() *_ProgramMap {
	fork := &_ProgramMap{
		n: self.n,
		m: self.m,
		b: make([]_ProgramEntry, len(self.b)),
	}
	copy(fork.b, self.b)
	return fork
}

func (self *_ProgramMap) get(vt *rt.GoType) interface{} {
	i := self.m + 1
	p := vt.Hash & self.m

	/* linear probing */
	for ; i > 0; i-- {
		if b := self.b[p]; b.vt == vt {
			return b.fn
		} else if b.vt == nil {
			break
		} else {
			p = (p + 1) & self.m
		}
	}

	/* not found */
	return nil
}

func (self *_ProgramMap) add(vt *rt.GoType, fn interface{}) *_ProgramMap {
	p := self.copy()
	f := float64(atomic.LoadUint64(&p.n)+1) / float64(p.m+1)

	/* check for load factor */
	if f > _LoadFactor {
		p = p.rehash()
	}

	/* insert the value */
	p.insert(vt, fn)
	return p
}

func (self *_ProgramMap) rehash() *_ProgramMap {
	c := (self.m + 1) << 1
	r := &_ProgramMap{m: c - 1, b: make([]_ProgramEntry, int(c))}

	/* rehash every entry */
	for i := uint32(0); i <= self.m; i++ {
		if b := self.b[i]; b.vt != nil {
			r.insert(b.vt, b.fn)
		}
	}

	/* rebuild successful */
	return r
}

type ProgramBatchItem struct {
	Vt *rt.GoType
	Fn interface{}
}

func (self *_ProgramMap) addMany(items []ProgramBatchItem) *_ProgramMap {
	missing := 0
	for i := range items {
		if items[i].Vt != nil && self.get(items[i].Vt) == nil {
			missing++
		}
	}
	if missing == 0 {
		return nil
	}
	targetCount := atomic.LoadUint64(&self.n) + uint64(missing)
	capNeeded := self.m + 1
	for float64(targetCount)/float64(capNeeded) > _LoadFactor {
		capNeeded <<= 1
	}
	var fork *_ProgramMap
	if capNeeded == self.m+1 {
		fork = self.copy()
	} else {
		fork = &_ProgramMap{
			m: capNeeded - 1,
			b: make([]_ProgramEntry, int(capNeeded)),
		}
		for i := uint32(0); i <= self.m; i++ {
			if b := self.b[i]; b.vt != nil {
				fork.insert(b.vt, b.fn)
			}
		}
	}
	for i := range items {
		vt := items[i].Vt
		if vt != nil && fork.get(vt) == nil {
			fork.insert(vt, items[i].Fn)
		}
	}
	return fork
}

func (self *_ProgramMap) insert(vt *rt.GoType, fn interface{}) {
	h := vt.Hash
	p := h & self.m

	/* linear probing */
	for i := uint32(0); i <= self.m; i++ {
		if b := &self.b[p]; b.vt != nil {
			p += 1
			p &= self.m
		} else {
			b.vt = vt
			b.fn = fn
			atomic.AddUint64(&self.n, 1)
			return
		}
	}

	/* should never happens */
	panic("no available slots")
}

/** RCU Program Cache with Per-Type Singleflight **/

type inflightProgram struct {
	wg   sync.WaitGroup
	val  interface{}
	err  error
	done bool
}

type ProgramCache struct {
	m        sync.Mutex
	p        unsafe.Pointer
	epoch    uint64
	inflight map[*rt.GoType]*inflightProgram
}

func CreateProgramCache() *ProgramCache {
	return &ProgramCache{
		p: unsafe.Pointer(newProgramMap()),
	}
}

func (self *ProgramCache) Reset() {
	self.m.Lock()
	self.epoch++
	self.inflight = nil
	atomic.StorePointer(&self.p, unsafe.Pointer(newProgramMap()))
	self.m.Unlock()
}

func (self *ProgramCache) Get(vt *rt.GoType) interface{} {
	return (*_ProgramMap)(atomic.LoadPointer(&self.p)).get(vt)
}

func (self *ProgramCache) Compute(vt *rt.GoType, compute func(*rt.GoType, ...interface{}) (interface{}, error), ex ...interface{}) (interface{}, error) {
	if val := self.Get(vt); val != nil {
		return val, nil
	}

	for {
		self.m.Lock()
		if val := self.Get(vt); val != nil {
			self.m.Unlock()
			return val, nil
		}
		if call, ok := self.inflight[vt]; ok {
			self.m.Unlock()
			call.wg.Wait()
			if call.done {
				return call.val, call.err
			}
			continue
		}
		if self.inflight == nil {
			self.inflight = make(map[*rt.GoType]*inflightProgram)
		}
		call := &inflightProgram{}
		call.wg.Add(1)
		self.inflight[vt] = call
		startEpoch := self.epoch
		self.m.Unlock()

		finished := false
		defer func() {
			if !finished {
				self.m.Lock()
				if self.inflight[vt] == call {
					delete(self.inflight, vt)
				}
				call.wg.Done()
				self.m.Unlock()
			}
		}()

		// Execute compilation outside self.m so distinct types compile concurrently.
		val, err := compute(vt, ex...)

		self.m.Lock()
		finished = true
		if self.inflight[vt] == call {
			delete(self.inflight, vt)
		}
		if err == nil && self.epoch == startEpoch {
			if existing := self.Get(vt); existing != nil {
				val = existing
			} else {
				cur := (*_ProgramMap)(atomic.LoadPointer(&self.p))
				atomic.StorePointer(&self.p, unsafe.Pointer(cur.add(vt, val)))
			}
		}
		call.val = val
		call.err = err
		call.done = true
		call.wg.Done()
		self.m.Unlock()
		return val, err
	}
}

func (self *ProgramCache) PutMany(items []ProgramBatchItem) {
	if len(items) == 0 {
		return
	}
	self.m.Lock()
	cur := (*_ProgramMap)(atomic.LoadPointer(&self.p))
	if next := cur.addMany(items); next != nil {
		atomic.StorePointer(&self.p, unsafe.Pointer(next))
	}
	self.m.Unlock()
}
