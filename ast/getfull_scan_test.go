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

package ast

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func getFullScanFixtures() []struct{ name, src string } {
	var compact, pretty bytes.Buffer
	if err := json.Compact(&compact, []byte(_TwitterJson)); err != nil {
		panic(err)
	}
	if err := json.Indent(&pretty, compact.Bytes(), "", "  "); err != nil {
		panic(err)
	}
	var fields strings.Builder
	fields.WriteByte('{')
	for i := 0; i < 1024; i++ {
		if i != 0 {
			fields.WriteByte(',')
		}
		fmt.Fprintf(&fields, `"field_%04d":{"name":"value_%04d","enabled":true,"count":12345}`, i, i)
	}
	fields.WriteByte('}')
	plain := `"` + strings.Repeat("continuous ascii text ", 2048) + `"`
	escaped := `"` + strings.Repeat(`ab\"cd\\ef\u4e2d`, 1024) + `"`
	deep := strings.Repeat(`{"items":[`, 64) + `{"id":123,"name":"middle"}` + strings.Repeat(`]}`, 64)
	tiny := "[" + strings.Repeat(`{"a":1,"b":"x"},`, 255) + `{"a":1,"b":"x"}]`
	var numbers, wide, mixed strings.Builder
	numbers.WriteByte('[')
	for i := 0; i < 1024; i++ {
		if i != 0 {
			numbers.WriteByte(',')
		}
		numbers.WriteString(strconv.Itoa(i))
	}
	numbers.WriteByte(']')
	floats := "[" + strings.Repeat("-12.345e+67,", 1023) + "-12.345e+67]"
	longInteger := strings.Repeat("9", 64)
	longIntegers := "[" + strings.Repeat(longInteger+",", 1023) + longInteger + "]"
	wide.WriteByte('[')
	for i := 0; i < 128; i++ {
		if i != 0 {
			wide.WriteByte(',')
		}
		wide.WriteString(strings.Repeat(" \t\r\n", 32))
		wide.WriteString(`{"key":true}`)
	}
	wide.WriteByte(']')
	mixed.WriteByte('[')
	for i := 0; i < 20; i++ {
		for _, size := range []int{0, 1, 7, 8, 15, 16, 31, 32, 33, 63, 64, 65, 255} {
			if mixed.Len() > 1 {
				mixed.WriteByte(',')
			}
			mixed.WriteString(`{"key":"` + strings.Repeat("x", size) + `"}`)
		}
	}
	mixed.WriteByte(']')
	escapedValues := "[" + strings.Repeat(`{"key":"ab\"cd\\ef\u4e2d"},`, 255) + `{"key":"ab\"cd\\ef\u4e2d"}]`
	return []struct{ name, src string }{
		{"TwitterCompact", compact.String()},
		{"TwitterPretty", pretty.String()},
		{"ManyFields", fields.String()},
		{"LongPlainString", plain},
		{"LongEscapedString", escaped},
		{"NestedContainers", deep},
		{"TinyObjects", tiny},
		{"NumberArray", numbers.String()},
		{"FloatArray", floats},
		{"LongIntegerArray", longIntegers},
		{"WideWhitespace", wide.String()},
		{"MixedStringLengths", mixed.String()},
		{"EscapedValues", escapedValues},
	}
}

func TestGetFullScanFixtures(t *testing.T) {
	for _, tc := range getFullScanFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			if !json.Valid([]byte(tc.src)) {
				t.Fatal("invalid fixture")
			}
			s := NewSearcher(tc.src)
			n, err := s.GetByPath()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := n.Raw()
			if err != nil || raw != tc.src {
				t.Fatalf("raw mismatch len=%d err=%v", len(raw), err)
			}
		})
	}
}

func BenchmarkGetFullFlowCoverage(b *testing.B) {
	for _, tc := range getFullScanFixtures() {
		b.Run(tc.name, func(b *testing.B) {
			s := NewSearcher(tc.src)
			n, err := s.GetByPath()
			if err != nil {
				b.Fatal(err)
			}
			raw, err := n.Raw()
			if err != nil || raw != tc.src {
				b.Fatal("fixture mismatch", err)
			}
			b.SetBytes(int64(len(tc.src)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				n, err = s.GetByPath()
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			if n.Type() != V_OBJECT && n.Type() != V_ARRAY && n.Type() != V_STRING {
				b.Fatal("unexpected node")
			}
		})
	}
}
