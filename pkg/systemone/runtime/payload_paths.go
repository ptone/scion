// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package runtime

import (
	"bytes"
	"encoding/json"
	"strings"
)

// narrowJSONPayloadByPaths extracts only the declared dotted paths from a JSON
// object payload and returns the canonical compact JSON subset. Non-object JSON
// roots or missing paths produce an empty JSON object "{}".
func narrowJSONPayloadByPaths(raw string, paths []string) (string, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return "", err
	}
	rootObj, ok := root.(map[string]any)
	if !ok {
		return "{}", nil
	}
	out := make(map[string]any)
	for _, p := range paths {
		segs := strings.Split(p, ".")
		val, found := lookupJSONPath(rootObj, segs)
		if !found {
			continue
		}
		setJSONPath(out, segs, val)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func lookupJSONPath(obj map[string]any, segs []string) (any, bool) {
	var cur any = obj
	for _, seg := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, exists := m[seg]
		if !exists {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func setJSONPath(dst map[string]any, segs []string, val any) {
	cur := dst
	for i := 0; i < len(segs)-1; i++ {
		seg := segs[i]
		existing, exists := cur[seg]
		if !exists {
			child := make(map[string]any)
			cur[seg] = child
			cur = child
			continue
		}
		child, ok := existing.(map[string]any)
		if !ok {
			// A broader parent path already populated this key; preserve it.
			return
		}
		cur = child
	}
	cur[segs[len(segs)-1]] = val
}
