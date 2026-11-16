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

package encoder

import (
	"encoding/json"
	"testing"
)

// thirdPartyAppender mimics a type from another library that happens to have
// a generic AppendJSON method next to its MarshalJSON.
type thirdPartyAppender struct{ V int }

func (thirdPartyAppender) AppendJSON(dst []byte) ([]byte, error) {
	return append(dst, `"append"`...), nil
}

func (thirdPartyAppender) MarshalJSON() ([]byte, error) {
	return []byte(`"marshal"`), nil
}

// TestGenericAppendJSONMethodIsIgnored checks that only the sonic-specific
// AppendSonicJSON method takes priority over MarshalJSON; a generic AppendJSON
// method must not change the output away from encoding/json.
func TestGenericAppendJSONMethodIsIgnored(t *testing.T) {
	v := struct {
		A thirdPartyAppender
		P *thirdPartyAppender
		S []thirdPartyAppender
		I interface{}
	}{P: &thirdPartyAppender{}, S: []thirdPartyAppender{{}}, I: thirdPartyAppender{}}
	want, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Encode(v, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("Encode = %s, encoding/json = %s", got, want)
	}
}
