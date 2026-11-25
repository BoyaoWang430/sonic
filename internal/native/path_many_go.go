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

package native

import (
	"github.com/bytedance/sonic/internal/native/pathplan"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/utils"
)

// LocateMany locates all paths in plan over src starting at base, writing
// results into out (len(out) >= plan.NumPaths()) without building AST nodes.
func LocateMany(src string, base int, plan *pathplan.Plan, validate bool, out []pathplan.LocatedSpan) (int, types.ParsingError) {
	n := plan.NumPaths()
	if n == 0 {
		return base, 0
	}
	for i := 0; i < n; i++ {
		out[i] = pathplan.LocatedSpan{Start: -1, End: -1, Found: false}
	}
	var block pathBlock
	remaining := n
	_, errCode, errPos := locatePlanNode(src, base, &plan.Root, &block, nil, validate, false, 0, out, &remaining)
	if errCode < 0 {
		return errPos, types.ParsingError(-errCode)
	}
	return 0, 0
}

func isValidJSONValueLead(c byte) bool {
	switch c {
	case '{', '[', '"', 't', 'f', 'n', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return true
	default:
		return false
	}
}

func trimSpanNumberSpace(src string, start, end int) int {
	if start < len(src) {
		c := src[start]
		if c == '-' || (c >= '0' && c <= '9') {
			for end > start && utils.IsSpace(src[end-1]) {
				end--
			}
		}
	}
	return end
}

func recordNodeTerminals(src string, start, end int, reqIndices []int, out []pathplan.LocatedSpan, remaining *int) {
	end = trimSpanNumberSpace(src, start, end)
	for _, idx := range reqIndices {
		out[idx] = pathplan.LocatedSpan{Start: start, End: end, Found: true}
	}
	*remaining -= len(reqIndices)
}

func locatePlanNode(
	src string,
	pos int,
	node *pathplan.Node,
	b *pathBlock,
	m *types.StateMachine,
	validate bool,
	inTarget bool,
	depth int,
	out []pathplan.LocatedSpan,
	remaining *int,
) (nextPos int, errCode int, errPos int) {
	var ch byte
	if uint(pos) < uint(len(src)) && src[pos] > ' ' {
		ch = src[pos]
		pos++
	} else if uint(pos+1) < uint(len(src)) && src[pos] == ' ' && src[pos+1] > ' ' {
		ch = src[pos+1]
		pos += 2
	} else {
		ch, pos = b.nextSlow(src, pos)
	}
	if ch == 0 {
		return pos, -int(types.ERR_EOF), pos
	}
	valStart := pos - 1
	if !isValidJSONValueLead(ch) {
		return valStart, -int(types.ERR_INVALID_CHAR), valStart
	}

	needSelf := len(node.ReqIndices) > 0
	inTarget = inTarget || needSelf
	hasKeyChildren := len(node.KeyEdges) > 0
	hasIdxChildren := len(node.IdxEdges) > 0

	// Pure terminal target node: skip/validate value once and record span.
	if !hasKeyChildren && !hasIdxChildren {
		var start, end int
		if validate {
			start, end = b.validateTargetAtDepth(src, valStart, m, depth)
			if start < 0 {
				return end, start, end
			}
		} else {
			end, start = b.skipValue(src, valStart)
			if start < 0 {
				return end, start, end
			}
		}
		recordNodeTerminals(src, start, end, node.ReqIndices, out, remaining)
		return end, 0, 0
	}

	// Object branch traversal.
	if ch == '{' && hasKeyChildren {
		if depth >= types.MAX_RECURSE {
			return pos, -int(types.ERR_RECURSE_EXCEED_MAX), pos
		}
		// Any array-index edges on this same node cannot match an object.
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

		if uint(pos) < uint(len(src)) && src[pos] > ' ' {
			ch = src[pos]
			pos++
		} else {
			ch, pos = b.nextSlow(src, pos)
		}
		if ch == '}' {
			*remaining -= unmatchedTargets
			if needSelf {
				recordNodeTerminals(src, valStart, pos, node.ReqIndices, out, remaining)
			}
			return pos, 0, 0
		}

		for {
			if ch != '"' {
				code, ep := pathError(pos, ch, types.ERR_INVALID_CHAR)
				return ep, code, ep
			}

			keyBegin := pos
			endKey, escape, kerr := b.string(src, keyBegin)
			if kerr != 0 {
				return endKey, -int(kerr), endKey
			}
			if depth+1 >= types.MAX_RECURSE {
				return endKey, -int(types.ERR_RECURSE_EXCEED_MAX), endKey
			}
			pos = endKey

			matchedEdge := -1
			if unmatchedTargets > 0 {
				if escape < 0 {
					idx := node.LookupPlainKey(src[keyBegin : endKey-1])
					if idx >= 0 && !pathplan.IsEdgeMatched(idx, matchedMask, matchedOverflow) {
						matchedEdge = idx
					}
				} else {
					for idx := range node.KeyEdges {
						if pathplan.IsEdgeMatched(idx, matchedMask, matchedOverflow) {
							continue
						}
						ep, ok, uerr := matchEscapedPathKey(src, node.KeyEdges[idx].Key, keyBegin, endKey)
						if uerr != 0 {
							return ep, -int(uerr), ep
						}
						if ok {
							matchedEdge = idx
							break
						}
					}
				}
			}

			if uint(pos) < uint(len(src)) && src[pos] > ' ' {
				ch = src[pos]
				pos++
			} else {
				ch, pos = b.nextSlow(src, pos)
			}
			if ch != ':' {
				code, ep := pathError(pos, ch, types.ERR_INVALID_CHAR)
				return ep, code, ep
			}

			if matchedEdge >= 0 {
				pathplan.MarkEdgeMatched(matchedEdge, &matchedMask, matchedOverflow)
				child := node.KeyEdges[matchedEdge].Child
				unmatchedTargets -= child.SubtreeTargets
				var cCode, cErrPos int
				pos, cCode, cErrPos = locatePlanNode(src, pos, child, b, m, validate, inTarget, depth+1, out, remaining)
				if cCode < 0 {
					return cErrPos, cCode, cErrPos
				}
				if *remaining == 0 && !inTarget {
					return pos, 0, 0
				}
			} else if inTarget && validate {
				p := pos
				ret := ValidateOneAtDepth(&src, &p, m, 0, depth+1)
				if ret < 0 {
					return p, ret, p
				}
				pos = p
			} else {
				var ret int
				pos, ret = b.skipValue(src, pos)
				if ret < 0 {
					return pos, ret, pos
				}
			}

			if uint(pos) < uint(len(src)) && src[pos] > ' ' {
				ch = src[pos]
				pos++
			} else {
				ch, pos = b.nextSlow(src, pos)
			}
			if ch == '}' {
				*remaining -= unmatchedTargets
				if needSelf {
					recordNodeTerminals(src, valStart, pos, node.ReqIndices, out, remaining)
				}
				return pos, 0, 0
			}
			if ch != ',' {
				code, ep := pathError(pos, ch, types.ERR_INVALID_CHAR)
				return ep, code, ep
			}
			if uint(pos) < uint(len(src)) && src[pos] > ' ' {
				ch = src[pos]
				pos++
			} else {
				ch, pos = b.nextSlow(src, pos)
			}
		}
	}

	// Array branch traversal.
	if ch == '[' && hasIdxChildren {
		if depth >= types.MAX_RECURSE {
			return pos, -int(types.ERR_RECURSE_EXCEED_MAX), pos
		}
		for _, ke := range node.KeyEdges {
			*remaining -= ke.Child.SubtreeTargets
		}

		unmatchedTargets := 0
		for _, ie := range node.IdxEdges {
			unmatchedTargets += ie.Child.SubtreeTargets
		}

		if uint(pos) < uint(len(src)) && src[pos] > ' ' {
			ch = src[pos]
			pos++
		} else {
			ch, pos = b.nextSlow(src, pos)
		}
		if ch == ']' {
			*remaining -= unmatchedTargets
			if needSelf {
				recordNodeTerminals(src, valStart, pos, node.ReqIndices, out, remaining)
			}
			return pos, 0, 0
		}
		if ch == 0 {
			return pos, -int(types.ERR_EOF), pos
		}
		pos--

		edgeCursor := 0
		for elemIdx := int64(0); ; elemIdx++ {
			if edgeCursor < len(node.IdxEdges) && node.IdxEdges[edgeCursor].Index == elemIdx {
				child := node.IdxEdges[edgeCursor].Child
				edgeCursor++
				unmatchedTargets -= child.SubtreeTargets
				var cCode, cErrPos int
				pos, cCode, cErrPos = locatePlanNode(src, pos, child, b, m, validate, inTarget, depth+1, out, remaining)
				if cCode < 0 {
					return cErrPos, cCode, cErrPos
				}
				if *remaining == 0 && !inTarget {
					return pos, 0, 0
				}
			} else if inTarget && validate {
				p := pos
				ret := ValidateOneAtDepth(&src, &p, m, 0, depth+1)
				if ret < 0 {
					return p, ret, p
				}
				pos = p
			} else {
				var ret int
				pos, ret = b.skipValue(src, pos)
				if ret < 0 {
					return pos, ret, pos
				}
			}

			if uint(pos) < uint(len(src)) && src[pos] > ' ' {
				ch = src[pos]
				pos++
			} else {
				ch, pos = b.nextSlow(src, pos)
			}
			if ch == ']' {
				*remaining -= unmatchedTargets
				if needSelf {
					recordNodeTerminals(src, valStart, pos, node.ReqIndices, out, remaining)
				}
				return pos, 0, 0
			}
			if ch != ',' {
				code, ep := pathError(pos, ch, types.ERR_INVALID_CHAR)
				return ep, code, ep
			}
			if depth+1 >= types.MAX_RECURSE {
				return pos, -int(types.ERR_RECURSE_EXCEED_MAX), pos
			}
		}
	}

	// Valid JSON value whose kind does not match child branches (e.g., null or
	// scalar where sub-fields were requested): skip/validate it cleanly and mark
	// descendant paths as missing without aborting sibling paths.
	var start, end int
	if validate {
		start, end = b.validateTargetAtDepth(src, valStart, m, depth)
		if start < 0 {
			return end, start, end
		}
	} else {
		end, start = b.skipValue(src, valStart)
		if start < 0 {
			return end, start, end
		}
	}
	childTargets := node.SubtreeTargets - len(node.ReqIndices)
	*remaining -= childTargets
	if needSelf {
		recordNodeTerminals(src, start, end, node.ReqIndices, out, remaining)
	}
	return end, 0, 0
}
