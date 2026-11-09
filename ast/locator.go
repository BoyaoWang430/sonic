/*
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

package ast

import (
	"errors"

	"github.com/bytedance/sonic/internal/native/types"
)

// ErrUnsupportedPathType is returned when a path component is neither string nor int >= 0.
var ErrUnsupportedPathType = errors.New("path must be either int(>=0) or string")

// inputView explicitly binds located byte spans to a concrete source string and
// base offset so spans never outlive or dangle across session buffer changes.
type inputView struct {
	src  string
	base int
}

type locatedSpan struct {
	start int
	end   int
	found bool
}

// PathPlan is an immutable, reusable compiled prefix plan for multi-path search
// and raw-span patching.
type PathPlan struct {
	numPaths int
	native   nativePathPlanHandle
}

// NumPaths returns the number of paths compiled into the plan.
func (p *PathPlan) NumPaths() int {
	if p == nil {
		return 0
	}
	return p.numPaths
}

// CompilePathPlan compiles a fixed list of JSON paths into a shared-prefix plan.
func CompilePathPlan(paths ...[]interface{}) (*PathPlan, error) {
	h, err := compilePathPlanImpl(paths)
	if err != 0 {
		return nil, ErrUnsupportedPathType
	}
	return &PathPlan{
		numPaths: len(paths),
		native:   h,
	}, nil
}

// MustCompilePathPlan compiles paths into a PathPlan and panics on invalid path element types.
func MustCompilePathPlan(paths ...[]interface{}) *PathPlan {
	p, err := CompilePathPlan(paths...)
	if err != nil {
		panic(err.Error())
	}
	return p
}

func locateWithPlan(view inputView, plan *PathPlan, validate bool, out []locatedSpan) error {
	if plan == nil || plan.numPaths == 0 {
		return nil
	}
	errPos, perr := locateSpansImpl(view, plan.native, validate, out)
	if perr != 0 {
		if perr == types.ERR_UNSUPPORT_TYPE {
			panic("path must be either int(>=0) or string")
		}
		p := Parser{s: view.src, p: errPos}
		return p.syntaxError(perr)
	}
	return nil
}
