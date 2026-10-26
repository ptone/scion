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

// Aliases for symbols moved to
// github.com/GoogleCloudPlatform/scion/pkg/hub/webchat
// by hack/pkgmove, so existing references in package hub keep compiling.

import (
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/webchat"
)

const (
	scheduledMessageTableMigration = webchat.ScheduledMessageTableMigration
)

func generateSnippet(p0 string, p1 string, p2 int) string {
	return webchat.GenerateSnippet(p0, p1, p2)
}

func sqliteScheduledTime(p0 time.Time) string {
	return webchat.SqliteScheduledTime(p0)
}

func sqliteStarTime(p0 time.Time) string {
	return webchat.SQLiteStarTime(p0)
}
