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

package ast

import (
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// SearchOptions controls Searcher's behavior
type SearchOptions struct {
	// ValidateJSON indicates the searcher to validate the entire JSON
	ValidateJSON bool

	// CopyReturn indicates the searcher to copy the result JSON instead of refer from the input
	// This can help to reduce memory usage if you cache the results
	CopyReturn bool

	// ConcurrentRead indicates the searcher to return a concurrently-READ-safe node,
	// including: GetByPath/Get/Index/GetOrIndex/Int64/Bool/Float64/String/Number/Interface/Array/Map/Raw/MarshalJSON
	ConcurrentRead bool
}

type Searcher struct {
	parser Parser
	SearchOptions
}

func NewSearcher(str string) *Searcher {
	return &Searcher{
		parser: Parser{
			s:      str,
			noLazy: false,
		},
		SearchOptions: SearchOptions{
			ValidateJSON: true,
		},
	}
}

// GetByPathCopy search in depth from top json and returns a **Copied** json node at the path location
func (self *Searcher) GetByPathCopy(path ...interface{}) (Node, error) {
	return self.getByPathOpts(true, path...)
}

// GetByPathNoCopy search in depth from top json and returns a **Referenced** json node at the path location
//
// WARN: this search directly refer partial json from top json, which has faster speed,
// may consumes more memory.
func (self *Searcher) GetByPath(path ...interface{}) (Node, error) {
	return self.getByPathOpts(self.CopyReturn, path...)
}

// GetManyByPath searches multiple paths in a single shared-prefix traversal and
// returns results strictly in caller request order. A missing individual path
// yields an empty V_NONE Node (!Exists()) at its slot without aborting sibling
// paths; malformed JSON returns a SyntaxError.
func (self *Searcher) GetManyByPath(paths ...[]interface{}) ([]Node, error) {
	return self.getManyByPath(self.CopyReturn, paths...)
}

// GetManyByPathCopy is like GetManyByPath with CopyReturn forced true so each
// matched Node owns an independent heap string copy without sibling retention.
func (self *Searcher) GetManyByPathCopy(paths ...[]interface{}) ([]Node, error) {
	return self.getManyByPath(true, paths...)
}

func (self *Searcher) getManyByPath(copyReturn bool, paths ...[]interface{}) ([]Node, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	plan, err := CompilePathPlan(paths...)
	if err != nil {
		panic("path must be either int(>=0) or string")
	}
	return self.getManyWithPlan(plan, copyReturn)
}

// GetManyWithPlan executes a precompiled PathPlan against the Searcher's input.
func (self *Searcher) GetManyWithPlan(plan *PathPlan) ([]Node, error) {
	return self.getManyWithPlan(plan, self.CopyReturn)
}

func (self *Searcher) getManyWithPlan(plan *PathPlan, copyReturn bool) ([]Node, error) {
	if plan == nil || plan.NumPaths() == 0 {
		return nil, nil
	}
	n := plan.NumPaths()
	var stackSpans [16]locatedSpan
	var spans []locatedSpan
	if n <= len(stackSpans) {
		spans = stackSpans[:n]
	} else {
		spans = make([]locatedSpan, n)
	}
	view := inputView{src: self.parser.s, base: 0}
	if err := locateWithPlan(view, plan, self.ValidateJSON, spans); err != nil {
		return nil, err
	}

	nodes := make([]Node, n)
	for i := 0; i < n; i++ {
		sp := spans[i]
		if !sp.found || sp.start < 0 {
			continue
		}
		t := switchRawType(view.src[sp.start])
		if t == _V_NONE {
			continue
		}
		var raw string
		if copyReturn {
			// Allocate an independent backing string per Node so retaining one
			// small node never pins large sibling results or the source buffer.
			raw = string([]byte(view.src[sp.start:sp.end]))
		} else {
			raw = view.src[sp.start:sp.end]
		}
		nodes[i] = newRawNode(raw, t, self.ConcurrentRead)
	}
	return nodes, nil
}

// SearchMany locates a batch of JSON paths over src in a single shared-prefix
// pass and returns Nodes in caller request order.
func SearchMany(src string, opts SearchOptions, paths ...[]interface{}) ([]Node, error) {
	s := Searcher{
		parser:        Parser{s: src, noLazy: false},
		SearchOptions: opts,
	}
	return s.GetManyByPath(paths...)
}

func (self *Searcher) getByPath(path ...interface{}) (Node, error) {
	return self.getByPathOpts(self.CopyReturn, path...)
}

func (self *Searcher) getByPathOpts(copyReturn bool, path ...interface{}) (Node, error) {
	p := Parser{
		s:      self.parser.s,
		noLazy: self.parser.noLazy,
	}
	start, err := p.getByPath(self.ValidateJSON, path...)
	if err != 0 {
		// for compatibility with old version
		if err == types.ERR_NOT_FOUND {
			return Node{}, ErrNotExist
		}
		if err == types.ERR_UNSUPPORT_TYPE {
			panic("path must be either int(>=0) or string")
		}
		return Node{}, p.syntaxError(err)
	}

	t := switchRawType(p.s[start])
	if t == _V_NONE {
		return Node{}, p.ExportError(err)
	}

	// copy string to reducing memory usage
	var raw string
	if copyReturn {
		raw = rt.Mem2Str([]byte(p.s[start:p.p]))
	} else {
		raw = p.s[start:p.p]
	}
	return newRawNode(raw, t, self.ConcurrentRead), nil
}

// _GetByPath returns the start and end byte offsets and type of the target node.
func _GetByPath(src string, path ...interface{}) (start int, end int, typ int, err error) {
	p := NewParserObj(src)
	s, e := p.getByPath(false, path...)
	if e != 0 {
		// for compatibility with old version
		if e == types.ERR_NOT_FOUND {
			return -1, -1, 0, ErrNotExist
		}
		if e == types.ERR_UNSUPPORT_TYPE {
			panic("path must be either int(>=0) or string")
		}
		return -1, -1, 0, p.syntaxError(e)
	}

	t := switchRawType(p.s[s])
	if t == _V_NONE {
		return -1, -1, 0, ErrNotExist
	}
	if t == _V_NUMBER {
		p.p = 1 + backward(p.s, p.p-1)
	}
	return s, p.p, int(t), nil
}

// ValidSyntax check if a json has a valid JSON syntax,
// while not validate UTF-8 charset
func _ValidSyntax(json string) bool {
	p := NewParserObj(json)
	_, e := p.skip()
	if e != 0 {
		return false
	}
	if skipBlank(p.s, p.p) != -int(types.ERR_EOF) {
		return false
	}
	return true
}

// SkipFast skip a json value in fast-skip algs,
// while not strictly validate JSON syntax and UTF-8 charset.
func _SkipFast(src string, i int) (int, int, error) {
	p := NewParserObj(src)
	p.p = i
	s, e := p.skipFast()
	if e != 0 {
		return -1, -1, p.ExportError(e)
	}
	t := switchRawType(p.s[s])
	if t == _V_NONE {
		return -1, -1, ErrNotExist
	}
	if t == _V_NUMBER {
		p.p = 1 + backward(p.s, p.p-1)
	}
	return s, p.p, nil
}
