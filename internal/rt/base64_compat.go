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

package rt

import (
	"encoding/base64"
)

func DecodeBase64(raw []byte) ([]byte, error) {
	ret := make([]byte, base64.StdEncoding.DecodedLen(len(raw)))
	n, err := base64.StdEncoding.Decode(ret, raw)
	if err != nil {
		return nil, err
	}
	return ret[:n], nil
}

func EncodeBase64ToString(src []byte) string {
	return base64.StdEncoding.EncodeToString(src)
}

func EncodeBase64(buf []byte, src []byte) []byte {
	if len(src) == 0 {
		return append(buf, '"', '"')
	}
	buf = append(buf, '"')
	need := base64.StdEncoding.EncodedLen(len(src))
	if cap(buf)-len(buf) < need {
		tmp := make([]byte, len(buf), len(buf)+need*2)
		copy(tmp, buf)
		buf = tmp
	}
	base64.StdEncoding.Encode(buf[len(buf):cap(buf)], src)
	buf = buf[:len(buf)+need]
	buf = append(buf, '"')
	return buf
}
