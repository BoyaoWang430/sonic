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

package sonic

import (
	"errors"
)

// NoCopyRawMessage is a raw encoded JSON value that borrows from immutable string
// inputs (UnmarshalString / Decoder) when CopyString is disabled, and detaches a
// standalone copy when CopyString is enabled or when decoding via Unmarshal([]byte)
// or StreamDecoder. Treat decoded NoCopyRawMessage slices as read-only; use
// encoding/json.RawMessage when unconditional standalone ownership is required.
type NoCopyRawMessage []byte

// MarshalJSON returns m as the JSON encoding of m.
func (m NoCopyRawMessage) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	return m, nil
}

// UnmarshalJSON sets *m to a reference of data. NoCopy here!!!
func (m *NoCopyRawMessage) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errors.New("sonic.NoCopyRawMessage: UnmarshalJSON on nil pointer")
	}
	*m = data
	return nil
}
