//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package native

import (
	"reflect"
	"sort"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/utils"
)

// LocatedSpan represents a located JSON value byte range [Start, End) bound to
// a specific caller-owned source string.
type LocatedSpan struct {
	Start int
	End   int
	Found bool
}

type planKeyEdge struct {
	key   string
	child *planNode
}

type planIdxEdge struct {
	index int64
	child *planNode
}

type planNode struct {
	reqIndices     []int
	keyEdges       []planKeyEdge
	keyMap         map[string]int
	idxEdges       []planIdxEdge
	subtreeTargets int
}

// PathPlan is an immutable compiled prefix trie for multi-path JSON span location.
type PathPlan struct {
	numPaths int
	root     planNode
}

func (p *PathPlan) NumPaths() int {
	if p == nil {
		return 0
	}
	return p.numPaths
}

// CompilePathPlan compiles a fixed batch of paths into a shared-prefix trie.
func CompilePathPlan(paths [][]interface{}) (*PathPlan, types.ParsingError) {
	plan := &PathPlan{numPaths: len(paths)}
	for reqIdx, path := range paths {
		cur := &plan.root
		cur.subtreeTargets++
		for _, comp := range path {
			key, index, isKey, ok := parsePathStep(comp)
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
			cur.subtreeTargets++
		}
		cur.reqIndices = append(cur.reqIndices, reqIdx)
	}
	plan.root.finalize()
	return plan, 0
}

func parsePathStep(component interface{}) (key string, index int64, isKey bool, ok bool) {
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

func (n *planNode) getOrAddKey(key string) *planNode {
	for i := range n.keyEdges {
		if n.keyEdges[i].key == key {
			return n.keyEdges[i].child
		}
	}
	child := &planNode{}
	n.keyEdges = append(n.keyEdges, planKeyEdge{key: key, child: child})
	return child
}

func (n *planNode) getOrAddIndex(index int64) *planNode {
	for i := range n.idxEdges {
		if n.idxEdges[i].index == index {
			return n.idxEdges[i].child
		}
	}
	child := &planNode{}
	n.idxEdges = append(n.idxEdges, planIdxEdge{index: index, child: child})
	return child
}

func (n *planNode) finalize() {
	if len(n.keyEdges) > 8 {
		n.keyMap = make(map[string]int, len(n.keyEdges))
		for i, e := range n.keyEdges {
			n.keyMap[e.key] = i
		}
	}
	if len(n.idxEdges) > 1 {
		sort.Slice(n.idxEdges, func(i, j int) bool {
			return n.idxEdges[i].index < n.idxEdges[j].index
		})
	}
	for _, e := range n.keyEdges {
		e.child.finalize()
	}
	for _, e := range n.idxEdges {
		e.child.finalize()
	}
}

func (n *planNode) lookupPlainKey(rawKey string) int {
	if n.keyMap != nil {
		if idx, ok := n.keyMap[rawKey]; ok {
			return idx
		}
		return -1
	}
	for i := range n.keyEdges {
		if n.keyEdges[i].key == rawKey {
			return i
		}
	}
	return -1
}

// LocateMany locates all paths in plan over src starting at base, writing
// results into out (len(out) >= plan.NumPaths()) without building AST nodes.
func LocateMany(src string, base int, plan *PathPlan, validate bool, out []LocatedSpan) (int, types.ParsingError) {
	if plan == nil || plan.numPaths == 0 {
		return base, 0
	}
	for i := 0; i < plan.numPaths; i++ {
		out[i] = LocatedSpan{Start: -1, End: -1, Found: false}
	}
	var block pathBlock
	remaining := plan.numPaths
	_, errCode, errPos := locatePlanNode(src, base, &plan.root, &block, nil, validate, false, 0, out, &remaining)
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

func recordNodeTerminals(src string, start, end int, reqIndices []int, out []LocatedSpan, remaining *int) {
	end = trimSpanNumberSpace(src, start, end)
	for _, idx := range reqIndices {
		out[idx] = LocatedSpan{Start: start, End: end, Found: true}
	}
	*remaining -= len(reqIndices)
}

func locatePlanNode(
	src string,
	pos int,
	node *planNode,
	b *pathBlock,
	m *types.StateMachine,
	validate bool,
	inTarget bool,
	depth int,
	out []LocatedSpan,
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

	needSelf := len(node.reqIndices) > 0
	inTarget = inTarget || needSelf
	hasKeyChildren := len(node.keyEdges) > 0
	hasIdxChildren := len(node.idxEdges) > 0

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
		recordNodeTerminals(src, start, end, node.reqIndices, out, remaining)
		return end, 0, 0
	}

	// Object branch traversal.
	if ch == '{' && hasKeyChildren {
		if depth >= types.MAX_RECURSE {
			return pos, -int(types.ERR_RECURSE_EXCEED_MAX), pos
		}
		// Any array-index edges on this same node cannot match an object.
		for _, ie := range node.idxEdges {
			*remaining -= ie.child.subtreeTargets
		}

		var matchedMask uint64
		var matchedOverflow []bool
		if len(node.keyEdges) > 64 {
			matchedOverflow = make([]bool, len(node.keyEdges))
		}
		unmatchedTargets := 0
		for _, ke := range node.keyEdges {
			unmatchedTargets += ke.child.subtreeTargets
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
				recordNodeTerminals(src, valStart, pos, node.reqIndices, out, remaining)
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
					idx := node.lookupPlainKey(src[keyBegin : endKey-1])
					if idx >= 0 && !isEdgeMatched(idx, matchedMask, matchedOverflow) {
						matchedEdge = idx
					}
				} else {
					for idx := range node.keyEdges {
						if isEdgeMatched(idx, matchedMask, matchedOverflow) {
							continue
						}
						ep, ok, uerr := matchEscapedPathKey(src, node.keyEdges[idx].key, keyBegin, endKey)
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
				markEdgeMatched(matchedEdge, &matchedMask, matchedOverflow)
				child := node.keyEdges[matchedEdge].child
				unmatchedTargets -= child.subtreeTargets
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
					recordNodeTerminals(src, valStart, pos, node.reqIndices, out, remaining)
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
		for _, ke := range node.keyEdges {
			*remaining -= ke.child.subtreeTargets
		}

		unmatchedTargets := 0
		for _, ie := range node.idxEdges {
			unmatchedTargets += ie.child.subtreeTargets
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
				recordNodeTerminals(src, valStart, pos, node.reqIndices, out, remaining)
			}
			return pos, 0, 0
		}
		if ch == 0 {
			return pos, -int(types.ERR_EOF), pos
		}
		pos--

		edgeCursor := 0
		for elemIdx := int64(0); ; elemIdx++ {
			if edgeCursor < len(node.idxEdges) && node.idxEdges[edgeCursor].index == elemIdx {
				child := node.idxEdges[edgeCursor].child
				edgeCursor++
				unmatchedTargets -= child.subtreeTargets
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
					recordNodeTerminals(src, valStart, pos, node.reqIndices, out, remaining)
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
	childTargets := node.subtreeTargets - len(node.reqIndices)
	*remaining -= childTargets
	if needSelf {
		recordNodeTerminals(src, start, end, node.reqIndices, out, remaining)
	}
	return end, 0, 0
}

func isEdgeMatched(idx int, mask uint64, overflow []bool) bool {
	if idx < 64 {
		return (mask & (uint64(1) << uint(idx))) != 0
	}
	return overflow[idx]
}

func markEdgeMatched(idx int, mask *uint64, overflow []bool) {
	if idx < 64 {
		*mask |= uint64(1) << uint(idx)
	} else {
		overflow[idx] = true
	}
}
