//go:build sonic_compat || (((!amd64 && !arm64) || go1.28 || !go1.20 || (arm64 && !go1.20)) && (!riscv64 || !go1.26 || go1.28 || !riscv64.rva22u64))

/*
* Copyright 2022 ByteDance Inc.
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
	"encoding/json"
	"unicode/utf8"

	"github.com/bytedance/sonic/encoder"
	"github.com/bytedance/sonic/internal/compat"
	"github.com/bytedance/sonic/internal/native/pathplan"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/unquote"
)

func init() {
	compat.Warn("sonic/ast")
}

func quote(buf *[]byte, val string) {
	quoteString(buf, val)
}

func quoteKey(buf *[]byte, val string) {
	quoteString(buf, val)
	*buf = append(*buf, ':')
}

func (self *Parser) decodeValue() (val types.JsonState) {
	e, v := decodeValue(self.s, self.p, self.dbuf == nil)
	if e < 0 {
		return v
	}
	self.p = e
	return v
}

// decodeKey returns the key's content start and first escape position.
// A negative start means that the next value is not a complete string.
func (self *Parser) decodeKey() (start int, ep int) {
	start = self.p
	if uint(start) >= uint(len(self.s)) || self.s[start] != '"' {
		start = skipBlank(self.s, start)
		if start < 0 || self.s[start] != '"' {
			// A non-string value can advance the cursor before the caller rejects
			// it as a key. Keep decodeValue's behavior on this uncommon path.
			self.decodeValue()
			return -1, -1
		}
	}

	end, ep := skipString(self.s, start)
	if end < 0 {
		return -1, -1
	}
	self.p = end
	return start + 1, ep
}

func (self *Parser) decodeStringTailStr(start, _ int) (string, types.ParsingError) {
	end, ep := skipString(self.s, start-1)
	if end < 0 {
		self.p = len(self.s)
		return "", types.ParsingError(-end)
	}
	self.p = end
	return self.decodeStringStr(int64(start), ep)
}

func (self *Parser) decodeStringTail(start, _ int) (Node, types.ParsingError) {
	end, ep := skipString(self.s, start-1)
	if end < 0 {
		self.p = len(self.s)
		return Node{}, types.ParsingError(-end)
	}
	self.p = end
	return self.decodeString(int64(start), ep)
}

func (self *Parser) skip() (int, types.ParsingError) {
	return self.skipDepth(0)
}

func (self *Parser) skipDepth(depth int) (int, types.ParsingError) {
	e, s := skipValueDepth(self.s, self.p, depth)
	if e < 0 {
		return self.p, types.ParsingError(-e)
	}
	self.p = e
	return s, 0
}

func (self *Parser) skipFast() (int, types.ParsingError) {
	e, s := skipValueFast(self.s, self.p)
	if e < 0 {
		return self.p, types.ParsingError(-e)
	}
	self.p = e
	return s, 0
}

func (self *Node) encodeInterface(buf *[]byte) error {
	out, err := json.Marshal(self.packAny())
	if err != nil {
		return err
	}
	*buf = append(*buf, out...)
	return nil
}

func (self *Parser) getByPath(validate bool, path ...interface{}) (int, types.ParsingError) {
	for _, p := range path {
		if idx, ok := p.(int); ok && idx >= 0 {
			if err := self.searchIndex(idx); err != 0 {
				return self.p, err
			}
		} else if key, ok := p.(string); ok {
			if err := self.searchKey(key); err != 0 {
				return self.p, err
			}
		} else {
			panic("path must be either int(>=0) or string")
		}
	}

	var start int
	var e types.ParsingError
	if validate {
		start, e = self.skip()
	} else {
		start, e = self.skipFast()
	}
	if e != 0 {
		return self.p, e
	}
	return start, 0
}

func validate_utf8(str string) bool {
	return utf8.ValidString(str)
}

func recordCompatTerminals(src string, start, end int, reqIndices []int, out []locatedSpan, remaining *int) {
	if start < len(src) && switchRawType(src[start]) == _V_NUMBER {
		end = 1 + backward(src, end-1)
	}
	for _, idx := range reqIndices {
		out[idx] = locatedSpan{start: start, end: end, found: true}
	}
	*remaining -= len(reqIndices)
}

func locateSpansImpl(view inputView, plan *pathplan.Plan, validate bool, out []locatedSpan) (int, types.ParsingError) {
	n := plan.NumPaths()
	if n == 0 {
		return 0, 0
	}
	for i := 0; i < n; i++ {
		out[i] = locatedSpan{start: -1, end: -1, found: false}
	}
	p := Parser{s: view.src, p: view.base}
	remaining := n
	if err := locateCompatPlanNode(&p, &plan.Root, validate, false, 0, out, &remaining); err != 0 {
		return p.p, err
	}
	return 0, 0
}

func locateCompatPlanNode(
	p *Parser,
	node *pathplan.Node,
	validate bool,
	inTarget bool,
	depth int,
	out []locatedSpan,
	remaining *int,
) types.ParsingError {
	ns := len(p.s)
	p.p = p.lspace(p.p)
	if p.p >= ns {
		return types.ERR_EOF
	}
	valStart := p.p
	c := p.s[valStart]
	if switchRawType(c) == _V_NONE {
		return types.ERR_INVALID_CHAR
	}

	needSelf := len(node.ReqIndices) > 0
	inTarget = inTarget || needSelf
	hasKeyChildren := len(node.KeyEdges) > 0
	hasIdxChildren := len(node.IdxEdges) > 0

	skipCurrentValue := func(d int) (int, types.ParsingError) {
		if validate {
			return p.skipDepth(d)
		}
		return p.skipFast()
	}

	if !hasKeyChildren && !hasIdxChildren {
		start, err := skipCurrentValue(depth)
		if err != 0 {
			return err
		}
		recordCompatTerminals(p.s, start, p.p, node.ReqIndices, out, remaining)
		return 0
	}

	if c == '{' && hasKeyChildren {
		if depth >= types.MAX_RECURSE {
			p.p++
			return types.ERR_RECURSE_EXCEED_MAX
		}
		for _, ie := range node.IdxEdges {
			*remaining -= ie.Child.SubtreeTargets
		}
		var matchedMask uint64
		var matchedOverflow []bool
		if len(node.KeyEdges) > 64 {
			matchedOverflow = make([]bool, len(node.KeyEdges))
		}
		unmatchedTargets := 0
		for _, ke := range node.KeyEdges {
			unmatchedTargets += ke.Child.SubtreeTargets
		}

		p.p++
		p.p = p.lspace(p.p)
		if p.p >= ns {
			return types.ERR_EOF
		}
		if p.s[p.p] == '}' {
			p.p++
			*remaining -= unmatchedTargets
			if needSelf {
				recordCompatTerminals(p.s, valStart, p.p, node.ReqIndices, out, remaining)
			}
			return 0
		}

		for {
			kStart, ep := p.decodeKey()
			if kStart < 0 {
				return types.ERR_INVALID_CHAR
			}
			if depth+1 >= types.MAX_RECURSE {
				return types.ERR_RECURSE_EXCEED_MAX
			}
			key := p.s[kStart : p.p-1]
			if ep != -1 {
				var uerr types.ParsingError
				if key, uerr = unquote.String(key); uerr != 0 {
					return uerr
				}
			}
			matchedEdge := -1
			if unmatchedTargets > 0 {
				if idx := node.LookupPlainKey(key); idx >= 0 && !pathplan.IsEdgeMatched(idx, matchedMask, matchedOverflow) {
					matchedEdge = idx
				}
			}
			if derr := p.delim(); derr != 0 {
				return derr
			}

			if matchedEdge >= 0 {
				pathplan.MarkEdgeMatched(matchedEdge, &matchedMask, matchedOverflow)
				child := node.KeyEdges[matchedEdge].Child
				unmatchedTargets -= child.SubtreeTargets
				if cerr := locateCompatPlanNode(p, child, validate, inTarget, depth+1, out, remaining); cerr != 0 {
					return cerr
				}
				if *remaining == 0 && !inTarget {
					return 0
				}
			} else {
				var serr types.ParsingError
				if inTarget && validate {
					_, serr = p.skipDepth(depth + 1)
				} else {
					_, serr = p.skipFast()
				}
				if serr != 0 {
					return serr
				}
			}

			p.p = p.lspace(p.p)
			if p.p >= ns {
				return types.ERR_EOF
			}
			switch p.s[p.p] {
			case '}':
				p.p++
				*remaining -= unmatchedTargets
				if needSelf {
					recordCompatTerminals(p.s, valStart, p.p, node.ReqIndices, out, remaining)
				}
				return 0
			case ',':
				p.p++
			default:
				return types.ERR_INVALID_CHAR
			}
		}
	}

	if c == '[' && hasIdxChildren {
		if depth >= types.MAX_RECURSE {
			p.p++
			return types.ERR_RECURSE_EXCEED_MAX
		}
		for _, ke := range node.KeyEdges {
			*remaining -= ke.Child.SubtreeTargets
		}
		unmatchedTargets := 0
		for _, ie := range node.IdxEdges {
			unmatchedTargets += ie.Child.SubtreeTargets
		}

		p.p++
		p.p = p.lspace(p.p)
		if p.p >= ns {
			return types.ERR_EOF
		}
		if p.s[p.p] == ']' {
			p.p++
			*remaining -= unmatchedTargets
			if needSelf {
				recordCompatTerminals(p.s, valStart, p.p, node.ReqIndices, out, remaining)
			}
			return 0
		}

		edgeCursor := 0
		for elemIdx := int64(0); ; elemIdx++ {
			if edgeCursor < len(node.IdxEdges) && node.IdxEdges[edgeCursor].Index == elemIdx {
				child := node.IdxEdges[edgeCursor].Child
				edgeCursor++
				unmatchedTargets -= child.SubtreeTargets
				if cerr := locateCompatPlanNode(p, child, validate, inTarget, depth+1, out, remaining); cerr != 0 {
					return cerr
				}
				if *remaining == 0 && !inTarget {
					return 0
				}
			} else {
				var serr types.ParsingError
				if inTarget && validate {
					_, serr = p.skipDepth(depth + 1)
				} else {
					_, serr = p.skipFast()
				}
				if serr != 0 {
					return serr
				}
			}

			p.p = p.lspace(p.p)
			if p.p >= ns {
				return types.ERR_EOF
			}
			switch p.s[p.p] {
			case ']':
				p.p++
				*remaining -= unmatchedTargets
				if needSelf {
					recordCompatTerminals(p.s, valStart, p.p, node.ReqIndices, out, remaining)
				}
				return 0
			case ',':
				p.p++
				if depth+1 >= types.MAX_RECURSE {
					return types.ERR_RECURSE_EXCEED_MAX
				}
			default:
				return types.ERR_INVALID_CHAR
			}
		}
	}

	start, err := skipCurrentValue(depth)
	if err != 0 {
		return err
	}
	*remaining -= node.SubtreeTargets - len(node.ReqIndices)
	if needSelf {
		recordCompatTerminals(p.s, start, p.p, node.ReqIndices, out, remaining)
	}
	return 0
}

func encodePatchValue(buf *[]byte, val interface{}) error {
	return encoder.EncodeInto(buf, val, encoder.NoEncoderNewline)
}

func validJSONBytes(b []byte) bool {
	return json.Valid(b)
}
