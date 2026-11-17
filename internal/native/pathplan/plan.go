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

// Package pathplan compiles multi-path JSON queries into an architecture-independent,
// read-only prefix trie shared by native, scalar, and compatibility executors.
package pathplan

import (
	"reflect"
	"sort"

	"github.com/bytedance/sonic/internal/native/types"
)

// LocatedSpan represents a located JSON value byte range [Start, End) bound to
// a caller-owned source string.
type LocatedSpan struct {
	Start int
	End   int
	Found bool
}

// KeyEdge represents a descending object-key transition in a compiled PathPlan.
type KeyEdge struct {
	Key   string
	Child *Node
}

// IdxEdge represents a descending array-index transition in a compiled PathPlan.
type IdxEdge struct {
	Index int64
	Child *Node
}

// Node is an immutable prefix-trie node holding only logical path steps and
// terminal request positions.
type Node struct {
	ReqIndices     []int
	KeyEdges       []KeyEdge
	KeyMap         map[string]int
	IdxEdges       []IdxEdge
	SubtreeTargets int
}

// Plan is an immutable compiled prefix trie for multi-path JSON span location.
type Plan struct {
	numPaths int
	Root     Node
}

// NumPaths returns the number of paths compiled into p.
func (p *Plan) NumPaths() int {
	if p == nil {
		return 0
	}
	return p.numPaths
}

// Compile compiles a fixed batch of paths into a shared-prefix trie.
func Compile(paths [][]interface{}) (*Plan, types.ParsingError) {
	plan := &Plan{numPaths: len(paths)}
	for reqIdx, path := range paths {
		cur := &plan.Root
		cur.SubtreeTargets++
		for _, comp := range path {
			key, index, isKey, ok := parseStep(comp)
			if !ok {
				return nil, types.ERR_UNSUPPORT_TYPE
			}
			if isKey {
				cur = cur.getOrAddKey(key)
			} else {
				if index < 0 {
					return nil, types.ERR_UNSUPPORT_TYPE
				}
				cur = cur.getOrAddIndex(index)
			}
			cur.SubtreeTargets++
		}
		cur.ReqIndices = append(cur.ReqIndices, reqIdx)
	}
	plan.Root.finalize()
	return plan, 0
}

func parseStep(component interface{}) (key string, index int64, isKey bool, ok bool) {
	switch v := component.(type) {
	case string:
		return v, 0, true, true
	case int:
		return "", int64(v), false, true
	default:
		if component == nil {
			return "", 0, false, false
		}
		val := reflect.ValueOf(component)
		switch val.Kind() {
		case reflect.String:
			return val.String(), 0, true, true
		case reflect.Int:
			return "", val.Int(), false, true
		default:
			return "", 0, false, false
		}
	}
}

func (n *Node) getOrAddKey(key string) *Node {
	for i := range n.KeyEdges {
		if n.KeyEdges[i].Key == key {
			return n.KeyEdges[i].Child
		}
	}
	child := &Node{}
	n.KeyEdges = append(n.KeyEdges, KeyEdge{Key: key, Child: child})
	return child
}

func (n *Node) getOrAddIndex(index int64) *Node {
	for i := range n.IdxEdges {
		if n.IdxEdges[i].Index == index {
			return n.IdxEdges[i].Child
		}
	}
	child := &Node{}
	n.IdxEdges = append(n.IdxEdges, IdxEdge{Index: index, Child: child})
	return child
}

func (n *Node) finalize() {
	if len(n.KeyEdges) > 8 {
		n.KeyMap = make(map[string]int, len(n.KeyEdges))
		for i, e := range n.KeyEdges {
			n.KeyMap[e.Key] = i
		}
	}
	if len(n.IdxEdges) > 1 {
		sort.Slice(n.IdxEdges, func(i, j int) bool {
			return n.IdxEdges[i].Index < n.IdxEdges[j].Index
		})
	}
	for _, e := range n.KeyEdges {
		e.Child.finalize()
	}
	for _, e := range n.IdxEdges {
		e.Child.finalize()
	}
}

// LookupPlainKey returns the KeyEdges index matching rawKey, or -1 if absent.
func (n *Node) LookupPlainKey(rawKey string) int {
	if n.KeyMap != nil {
		if idx, ok := n.KeyMap[rawKey]; ok {
			return idx
		}
		return -1
	}
	for i := range n.KeyEdges {
		if n.KeyEdges[i].Key == rawKey {
			return i
		}
	}
	return -1
}

// IsEdgeMatched reports whether key edge idx has already matched in the current object.
func IsEdgeMatched(idx int, mask uint64, overflow []bool) bool {
	if idx < 64 {
		return (mask & (uint64(1) << uint(idx))) != 0
	}
	return overflow[idx]
}

// MarkEdgeMatched marks key edge idx as matched in the current object.
func MarkEdgeMatched(idx int, mask *uint64, overflow []bool) {
	if idx < 64 {
		*mask |= uint64(1) << uint(idx)
	} else {
		overflow[idx] = true
	}
}
