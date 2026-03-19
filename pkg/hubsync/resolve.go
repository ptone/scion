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

package hubsync

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
	"github.com/google/uuid"
)

// IsHubGroveRef returns true if the given grove path value looks like a hub
// grove reference (slug, name, UUID, or git URL) rather than a filesystem path.
// It is used to decide whether to resolve the grove via the hub API instead of
// the local filesystem.
func IsHubGroveRef(grovePath string) bool {
	if grovePath == "" {
		return false
	}

	// "global" and "home" are special filesystem-like values
	if grovePath == "global" || grovePath == "home" {
		return false
	}

	// Git URLs are always hub references
	if util.IsGitURL(grovePath) {
		return true
	}

	// Absolute or explicitly relative paths are filesystem references
	if strings.HasPrefix(grovePath, "/") || strings.HasPrefix(grovePath, "./") || strings.HasPrefix(grovePath, "../") {
		return false
	}

	// Contains path separators → filesystem path
	if strings.Contains(grovePath, string(os.PathSeparator)) {
		return false
	}

	// Could be a slug or a relative directory name. Check the filesystem:
	// if the path exists as a directory or contains a .scion subdirectory,
	// treat it as a local path.
	if info, err := os.Stat(grovePath); err == nil && info.IsDir() {
		return false
	}
	if info, err := os.Stat(grovePath + "/.scion"); err == nil && info.IsDir() {
		return false
	}

	return true
}

// resolveHubGroveRef resolves a hub grove reference (slug, name, UUID, or git
// URL) by loading hub settings from a fallback grove and querying the hub API.
func resolveHubGroveRef(ref string, opts EnsureHubReadyOptions) (*HubContext, error) {
	debugf("resolveHubGroveRef: ref=%s", ref)

	// Load hub settings from the fallback grove (current project or global)
	fallbackPath, isGlobal, err := config.ResolveGrovePath("")
	if err != nil {
		return nil, fmt.Errorf("failed to resolve fallback grove for hub settings: %w", err)
	}
	debugf("resolveHubGroveRef: fallbackPath=%s, isGlobal=%v", fallbackPath, isGlobal)

	settings, err := config.LoadSettings(fallbackPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load settings from fallback grove: %w", err)
	}

	debugf("resolveHubGroveRef: hub=%v, hubConfigured=%v, hubEnabled=%v, hubExplicitlyDisabled=%v",
		settings.Hub != nil, settings.IsHubConfigured(), settings.IsHubEnabled(), settings.IsHubExplicitlyDisabled())
	if settings.Hub != nil {
		hasToken := settings.Hub.Token != ""
		hasAPIKey := settings.Hub.APIKey != ""
		hasEndpoint := settings.Hub.Endpoint != ""
		enabledPtr := "<nil>"
		if settings.Hub.Enabled != nil {
			enabledPtr = fmt.Sprintf("%v", *settings.Hub.Enabled)
		}
		debugf("resolveHubGroveRef: hub.enabled=%s, hub.endpoint=%v, hub.hasToken=%v, hub.hasAPIKey=%v",
			enabledPtr, hasEndpoint, hasToken, hasAPIKey)
	}

	if !settings.IsHubEnabled() {
		return nil, fmt.Errorf("hub grove references (slugs, names, git URLs) require hub mode to be enabled\n\n" +
			"Enable with: scion config set hub.enabled true")
	}

	endpoint := getEndpoint(settings)
	if endpoint == "" {
		return nil, fmt.Errorf("hub is enabled but no endpoint configured\n\nConfigure via: scion config set hub.endpoint <url>")
	}

	client, err := createHubClient(settings, endpoint)
	if err != nil {
		return nil, wrapHubError(fmt.Errorf("failed to create hub client: %w", err))
	}

	// Health check
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.Health(ctx); err != nil {
		return nil, wrapHubError(fmt.Errorf("hub at %s is not responding: %w", endpoint, err))
	}

	// Resolve the grove on the hub
	grove, err := resolveGroveOnHub(ctx, client, ref)
	if err != nil {
		return nil, err
	}

	brokerID := ""
	if settings.Hub != nil {
		brokerID = settings.Hub.BrokerID
	}

	hubCtx := &HubContext{
		Client:   client,
		Endpoint: endpoint,
		Settings: settings,
		GroveID:  grove.ID,
		BrokerID: brokerID,
		// Use the fallback grove path for settings access, not the target grove
		GrovePath: fallbackPath,
		IsGlobal:  isGlobal,
	}

	debugf("resolveHubGroveRef: resolved grove %s (ID: %s) via hub", grove.Name, grove.ID)
	return hubCtx, nil
}

// resolveGroveOnHub resolves a grove reference on the hub, trying multiple
// strategies in order: UUID, git URL, slug, name.
func resolveGroveOnHub(ctx context.Context, client hubclient.Client, ref string) (*hubclient.Grove, error) {
	// 1. Try as UUID
	if _, err := uuid.Parse(ref); err == nil {
		grove, err := client.Groves().Get(ctx, ref)
		if err == nil {
			return grove, nil
		}
		if !apiclient.IsNotFoundError(err) {
			return nil, fmt.Errorf("failed to get grove by ID: %w", err)
		}
		// UUID format but not found — fall through to other strategies
	}

	// 2. Try as git URL
	if util.IsGitURL(ref) {
		resp, err := client.Groves().List(ctx, &hubclient.ListGrovesOptions{
			GitRemote: util.NormalizeGitRemote(ref),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to search for grove by git URL: %w", err)
		}
		switch len(resp.Groves) {
		case 0:
			return nil, fmt.Errorf("no grove found for git URL '%s'", ref)
		case 1:
			return &resp.Groves[0], nil
		default:
			return nil, fmt.Errorf("multiple groves found for git URL '%s' — please use a grove ID or slug instead", ref)
		}
	}

	// 3. Try as slug
	resp, err := client.Groves().List(ctx, &hubclient.ListGrovesOptions{
		Slug: ref,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search for grove by slug: %w", err)
	}
	if len(resp.Groves) == 1 {
		return &resp.Groves[0], nil
	}

	// 4. Try as name
	resp, err = client.Groves().List(ctx, &hubclient.ListGrovesOptions{
		Name: ref,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search for grove by name: %w", err)
	}
	switch len(resp.Groves) {
	case 0:
		return nil, fmt.Errorf("grove '%s' not found on hub", ref)
	case 1:
		return &resp.Groves[0], nil
	default:
		return nil, fmt.Errorf("multiple groves found with name '%s' — please use a grove ID or slug instead", ref)
	}
}
