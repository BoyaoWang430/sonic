/**
 * Copyright 2023 ByteDance Inc.
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
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func makeNodes(l int) []Node {
	r := make([]Node, l)
	for i := 0; i < l; i++ {
		r[i] = NewBool(true)
	}
	return r
}

func makePairs(l int) []Pair {
	r := make([]Pair, l)
	for i := 0; i < l; i++ {
		r[i] = NewPair(strconv.Itoa(i), NewBool(true))
	}
	return r
}

func Test_linkedPairs_Push(t *testing.T) {
	type args struct {
		in  []Pair
		v   Pair
		exp []Pair
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "add empty",
			args: args{
				in:  []Pair{},
				v:   NewPair("a", NewBool(true)),
				exp: []Pair{NewPair("a", NewBool(true))},
			},
		},
		{
			name: "add one",
			args: args{
				in:  []Pair{NewPair("a", NewBool(false))},
				v:   NewPair("b", NewBool(true)),
				exp: []Pair{NewPair("a", NewBool(false)), NewPair("b", NewBool(true))},
			},
		},
		{
			name: "add _DEFAULT_NODE_CAP",
			args: args{
				in:  makePairs(_DEFAULT_NODE_CAP),
				v:   NewPair(strconv.Itoa(_DEFAULT_NODE_CAP), NewBool(true)),
				exp: makePairs(_DEFAULT_NODE_CAP + 1),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			self := &linkedPairs{}
			self.FromSlice(tt.args.in)
			self.Push(tt.args.v)
			act := make([]Pair, self.Len())
			self.ToSlice(act)
			require.Equal(t, tt.args.exp, act)
		})
	}
}

func Test_linkedNodes_Push(t *testing.T) {
	type args struct {
		in  []Node
		v   Node
		exp []Node
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "add empty",
			args: args{
				in:  []Node{},
				v:   NewBool(true),
				exp: []Node{NewBool(true)},
			},
		},
		{
			name: "add one",
			args: args{
				in:  []Node{NewBool(false)},
				v:   NewBool(true),
				exp: []Node{NewBool(false), NewBool(true)},
			},
		},
		{
			name: "add _DEFAULT_NODE_CAP",
			args: args{
				in:  makeNodes(_DEFAULT_NODE_CAP),
				v:   NewBool(true),
				exp: makeNodes(_DEFAULT_NODE_CAP + 1),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			self := &linkedNodes{}
			self.FromSlice(tt.args.in)
			self.Push(tt.args.v)
			act := make([]Node, self.Len())
			self.ToSlice(act)
			require.Equal(t, tt.args.exp, act)
		})
	}
}

func Test_linkedNodes_Pop(t *testing.T) {
	type args struct {
		in  []Node
		exp []Node
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "remove empty",
			args: args{
				in:  []Node{},
				exp: []Node{},
			},
		},
		{
			name: "remove one",
			args: args{
				in:  []Node{NewBool(false)},
				exp: []Node{},
			},
		},
		{
			name: "add _DEFAULT_NODE_CAP",
			args: args{
				in:  makeNodes(_DEFAULT_NODE_CAP),
				exp: makeNodes(_DEFAULT_NODE_CAP - 1),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			self := &linkedNodes{}
			self.FromSlice(tt.args.in)
			self.Pop()
			act := make([]Node, self.Len())
			self.ToSlice(act)
			require.Equal(t, tt.args.exp, act)
		})
	}
}

func Test_linkedNodes_MoveOne(t *testing.T) {
	type args struct {
		in     []Node
		source int
		target int
		exp    []Node
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "over index",
			args: args{
				in:     []Node{NewBool(true)},
				source: 1,
				target: 0,
				exp:    []Node{NewBool(true)},
			},
		},
		{
			name: "equal index",
			args: args{
				in:     []Node{NewBool(true)},
				source: 0,
				target: 0,
				exp:    []Node{NewBool(true)},
			},
		},
		{
			name: "forward index",
			args: args{
				in:     []Node{NewString("a"), NewString("b"), NewString("c")},
				source: 0,
				target: 2,
				exp:    []Node{NewString("b"), NewString("c"), NewString("a")},
			},
		},
		{
			name: "backward index",
			args: args{
				in:     []Node{NewString("a"), NewString("b"), NewString("c")},
				source: 2,
				target: 1,
				exp:    []Node{NewString("a"), NewString("c"), NewString("b")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			self := &linkedNodes{}
			self.FromSlice(tt.args.in)
			self.MoveOne(tt.args.source, tt.args.target)
			act := make([]Node, self.Len())
			self.ToSlice(act)
			require.Equal(t, tt.args.exp, act)
		})
	}
}

func TestLinkedPairsIndexTombstoneAndPointerStability(t *testing.T) {
	// Exceed _Threshold_Index (32) and _DEFAULT_NODE_CAP (16) with duplicate keys.
	pairs := make([]Pair, 0, 40)
	pairs = append(pairs, NewPair("dup", NewNumber("1")))
	for i := 0; i < 36; i++ {
		pairs = append(pairs, NewPair("k"+strconv.Itoa(i), NewNumber(strconv.Itoa(i+10))))
	}
	pairs = append(pairs, NewPair("dup", NewNumber("2")))

	obj := NewObject(pairs)
	heldFirst := obj.Get("k0")
	heldTail := obj.Get("k25")
	require.True(t, heldFirst.Exists())
	require.True(t, heldTail.Exists())

	// Indexed object resolves duplicate key to last live entry ("2").
	v, err := obj.Get("dup").Int64()
	require.NoError(t, err)
	require.Equal(t, int64(2), v)

	// Unsetting "dup" tombstones the last occurrence and re-points the index to "1",
	// while keeping held Node addresses across chunks completely stable.
	existed, err := obj.Unset("dup")
	require.NoError(t, err)
	require.True(t, existed)
	v, err = obj.Get("dup").Int64()
	require.NoError(t, err)
	require.Equal(t, int64(1), v)

	// UnsetByIndex also routes through linkedPairs.UnsetLive and updates the index.
	existed, err = obj.UnsetByIndex(0) // live index 0 is the remaining "dup":"1"
	require.NoError(t, err)
	require.True(t, existed)
	require.False(t, obj.Get("dup").Exists())

	// Appending additional keys grows tail chunks without invalidating held pointers.
	for i := 40; i < 70; i++ {
		_, err := obj.Set("extra"+strconv.Itoa(i), NewNumber(strconv.Itoa(i)))
		require.NoError(t, err)
	}
	require.Same(t, heldFirst, obj.Get("k0"))
	require.Same(t, heldTail, obj.Get("k25"))

	// Sorting rebuilds the index cleanly so lookups remain accurate.
	require.NoError(t, obj.SortKeys(false))
	k25Val, err := obj.Get("k25").Int64()
	require.NoError(t, err)
	require.Equal(t, int64(35), k25Val)
}
