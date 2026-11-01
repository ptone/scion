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

package hub

import (
	"reflect"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// TestServerConfigUpdateRequestCannotCarryRuntimeOptOut pins that the
// server-config PUT cannot write runtime.allow_unintercepted_metadata into
// the global settings file: ServerConfigUpdateRequest has no top-level
// `runtime` member, and no type reachable from it is the runtime defaults
// block, the opt-out, or a whole VersionedSettings that would carry them.
// The file-mode write path changes only the members the request decodes
// (applySettingsUpdatesFromBody), so a body that sends `runtime` is dropped.
// The walk is over static types, so it cannot see through an interface
// (any) field; it assumes the request holds only concrete settings types.
func TestServerConfigUpdateRequestCannotCarryRuntimeOptOut(t *testing.T) {
	reqType := reflect.TypeOf(ServerConfigUpdateRequest{})
	for i := 0; i < reqType.NumField(); i++ {
		f := reqType.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			name = f.Name
		}
		if strings.EqualFold(name, "runtime") {
			t.Errorf("ServerConfigUpdateRequest.%s decodes the top-level %q member; the runtime section holds the operator-only runtime.allow_unintercepted_metadata and must not be API-writable", f.Name, name)
		}
	}

	forbidden := map[reflect.Type]bool{
		reflect.TypeOf(config.V1RuntimeDefaultsConfig{}):       true,
		reflect.TypeOf(config.V1UninterceptedMetadataOptOut{}): true,
		reflect.TypeOf(config.VersionedSettings{}):             true,
	}
	seen := map[reflect.Type]bool{}
	var walk func(typ reflect.Type, path string)
	walk = func(typ reflect.Type, path string) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		if forbidden[typ] {
			t.Errorf("ServerConfigUpdateRequest reaches %s at %s; the server-config PUT must not be able to carry runtime.allow_unintercepted_metadata", typ, path)
			return
		}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(typ.Elem(), path+"[]")
		case reflect.Map:
			walk(typ.Key(), path+"{key}")
			walk(typ.Elem(), path+"{}")
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				walk(f.Type, path+"."+f.Name)
			}
		}
	}
	walk(reqType, "ServerConfigUpdateRequest")
	if len(seen) < 10 {
		t.Fatalf("walked only %d types; the guard would pass vacuously", len(seen))
	}
}
