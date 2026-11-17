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

package resolver

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type bas struct {
	Y int `json:"Y"`
}

type bat struct {
	bas
}

type bau struct {
	Y int
}

type bay struct {
	Y int `json:"Y"`
}

type baz struct {
	Y int `json:"W"`
}

type bar struct {
	bat
	bau
	*bay
	baz
}

type PackageError struct {
	ImportStack      []string
	Pos              string
	Err              error
	IsImportCycle    bool
	Hard             bool
	alwaysPrintStack bool
	Y                *int
}

type Foo struct {
	X int
	*PackageError
	bar
}

func TestResolver_ResolveStruct(t *testing.T) {
	for _, fv := range ResolveStruct(reflect.TypeOf(Foo{})) {
		println(fv.String())
	}
}

func TestResolverHasNoEncoderOrKernelDependency(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	forbidden := []string{
		"github.com/bytedance/sonic/internal/encoder",
		"github.com/bytedance/sonic/internal/decoder",
		"github.com/bytedance/sonic/internal/native",
		"unsafe",
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if p == bad || strings.HasPrefix(p, bad+"/") {
					t.Fatalf("resolver file %s imports forbidden package %q", name, p)
				}
			}
		}
	}
}
