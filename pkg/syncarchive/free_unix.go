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

//go:build unix

package syncarchive

import (
	"math"

	"golang.org/x/sys/unix"
)

func availableBytes(dir string) (int64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	// Bsize is signed on some platforms and unsigned on others.
	avail := uint64(st.Bavail) * uint64(st.Bsize)
	if avail > math.MaxInt64 {
		return math.MaxInt64, true
	}
	return int64(avail), true
}
