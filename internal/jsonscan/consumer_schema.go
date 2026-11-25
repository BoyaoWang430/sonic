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

type planFrame struct {
	plan        *Plan
	expectedIdx int
}

// SchemaState owns static Schema projection plan frames and field-selection
// policy without owning independent JSON container scanning loops.
type SchemaState struct {
	dirty              bool
	CaseSensitive      bool
	DisallowUnknown    bool
	RelaxedUnknownSkip bool
	HasPlan            bool
	ActivePlan         *Plan
	PlanDepth          int
	InlinePlans        [32]planFrame
	HeapPlans          []planFrame
}

func (s *SchemaState) reset() {
	if s.dirty {
		s.resetSlow()
	}
}

//go:noinline
func (s *SchemaState) resetSlow() {
	clearPlans := s.PlanDepth
	if clearPlans > len(s.InlinePlans) {
		clearPlans = len(s.InlinePlans)
	}
	for i := 0; i < clearPlans; i++ {
		s.InlinePlans[i] = planFrame{}
	}
	for i := range s.HeapPlans {
		s.HeapPlans[i] = planFrame{}
	}
	s.dirty = false
	s.CaseSensitive = false
	s.DisallowUnknown = false
	s.RelaxedUnknownSkip = false
	s.HasPlan = false
	s.ActivePlan = nil
	s.PlanDepth = 0
}

func (s *SchemaState) configure(plan *Plan, caseSensitive, disallowUnknown, relaxedUnknownSkip bool) {
	s.dirty = true
	s.HasPlan = plan != nil
	s.ActivePlan = plan
	s.CaseSensitive = caseSensitive
	s.DisallowUnknown = disallowUnknown
	s.RelaxedUnknownSkip = relaxedUnknownSkip
}

func (s *SchemaState) pushPlanFrame(p *Plan) {
	s.dirty = true
	d := s.PlanDepth
	if d < len(s.InlinePlans) {
		s.InlinePlans[d] = planFrame{plan: p}
	} else {
		hi := d - len(s.InlinePlans)
		if hi < len(s.HeapPlans) {
			s.HeapPlans[hi] = planFrame{plan: p}
		} else {
			s.HeapPlans = append(s.HeapPlans, planFrame{plan: p})
		}
	}
	s.PlanDepth = d + 1
}

func (s *SchemaState) topPlanFrame() *planFrame {
	d := s.PlanDepth - 1
	if d < len(s.InlinePlans) {
		return &s.InlinePlans[d]
	}
	return &s.HeapPlans[d-len(s.InlinePlans)]
}

func (s *SchemaState) popPlanFrame() {
	s.PlanDepth--
}

func (m *Machine) prepareArrayElemPlan() {
	if m.isEmitting() && m.Schema.HasPlan && m.Schema.PlanDepth > 0 {
		if pf := m.Schema.topPlanFrame(); pf.plan != nil {
			m.Schema.ActivePlan = pf.plan.Elem
		}
	}
}

// applySchemaKeySelection resolves an emitted object key against the active
// SchemaState. Returns (emitKeyNode, updatedKeyNode). Consumer never alters
// LexState cursors.
func (m *Machine) applySchemaKeySelection(keyNode Node, keyStr string, keyASCII bool) (bool, Node) {
	s := &m.Schema
	g := &m.Grammar
	if !s.HasPlan || s.PlanDepth <= 0 {
		return true, keyNode
	}
	pf := s.topPlanFrame()
	if pf.plan == nil {
		s.ActivePlan = nil
		return true, keyNode
	}
	switch pf.plan.Kind {
	case PlanStruct:
		sm := pf.plan.Struct
		nfields := sm.FieldCount()
		idx := -1
		if pf.expectedIdx < nfields && sm.FieldName(pf.expectedIdx) == keyStr {
			idx = pf.expectedIdx
			pf.expectedIdx = idx + 1
		} else {
			if !keyASCII && !s.CaseSensitive {
				keyASCII = isShortASCII(keyStr)
			}
			idx = sm.MatchField(keyStr, s.CaseSensitive, keyASCII)
			if idx >= 0 {
				pf.expectedIdx = idx + 1
			}
		}
		if idx < 0 {
			g.SubModeActive = true
			g.SubFloorDepth = g.Depth
			g.SubStart = -1
			if s.DisallowUnknown {
				g.SubMode = ModeValidateOnly
				g.SubDisUnknown = true
				g.SubKeyNode = keyNode
			} else {
				m.rollbackUnemittedEscapedKey(keyNode)
				if s.RelaxedUnknownSkip {
					g.SubMode = ModeRelaxedSkip
					g.SubDisUnknown = false
				} else {
					g.SubMode = ModeValidateOnly
					g.SubDisUnknown = false
				}
			}
			s.ActivePlan = nil
			return false, keyNode
		}
		if idx < 0xFFFFFE {
			keyNode.Typ |= uint64(idx+1) << 8
		}
		s.ActivePlan = pf.plan.Fields[idx]
		return true, keyNode
	case PlanMap:
		s.ActivePlan = pf.plan.Elem
		return true, keyNode
	default:
		s.ActivePlan = nil
		return true, keyNode
	}
}
