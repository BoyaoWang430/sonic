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

package jsonscan

// stepLiteral advances `true`, `false`, or `null` matching across chunk boundaries.
func (m *Machine) stepLiteral(src string, final bool) (done bool, res ScanResult, emitNode bool, nd Node) {
	g := &m.Grammar
	l := &m.Lex
	tokPos := l.TokStart
	want := "null"
	kind := KNull
	if l.LitChar == 't' {
		want, kind = "true", KTrue
	} else if l.LitChar == 'f' {
		want, kind = "false", KFalse
	}
	if final && g.Profile.ASTCompatLex && len(src)-tokPos < len(want) {
		return false, m.failEOF(src, final), false, Node{}
	}
	idx := int(l.LitMatched)
	cur := g.Cur
	for idx < len(want) && cur < len(src) {
		if src[cur] != want[idx] {
			nextCur := cur + 1
			if g.Profile.ASTCompatLex {
				nextCur = cur
			}
			return false, m.failAt(src, final, cur, nextCur, ErrInvalidLiteral), false, Node{}
		}
		cur++
		idx++
	}
	g.Cur = cur
	l.LitMatched = uint8(idx)
	if idx < len(want) {
		if !final {
			return false, m.finishResult(src, final, StopNeedInput, ErrNone), false, Node{}
		}
		return false, m.failEOF(src, final), false, Node{}
	}
	l.LitMatched = 0
	if m.isEmitting() {
		if !CheckSourceSpan(tokPos, len(want), len(src)) {
			return false, m.failAt(src, final, tokPos, cur, ErrSpanOverflow), false, Node{}
		}
		return true, ScanResult{}, true, Node{Typ: kind | uint64(tokPos)<<PosBits}
	}
	return true, ScanResult{}, false, Node{}
}
