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

package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/spf13/cobra"
)

var projectGitHubPermissionsCmd = &cobra.Command{
	Use:   "permissions",
	Short: "Inspect and configure the project's GitHub App token permissions",
	Long: `Show the project's requested, App-granted, and effective GitHub App
installation token permissions, or switch between custom and default permissions.

Examples:
  scion project github permissions
  scion project github permissions set contents=write pull_requests=write issues=read metadata=read
  scion project github permissions reset`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, projectID, err := resolveProjectForGitHub()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		return runProjectGitHubPermissionsShow(ctx, client.Projects(), projectID, cmd.OutOrStdout())
	},
}

var projectGitHubPermissionsSetCmd = &cobra.Command{
	Use:   "set <permission=level>...",
	Short: "Set custom GitHub App token permissions (full replacement)",
	Long: `Replace the project's GitHub App token permissions with a custom set.

Each argument must have the form <key>=<level> where key is one of:
  contents, pull_requests, issues, metadata, checks, actions
and level is one of:
  none, read, write

At least one permission must be set to "read" or "write". Unspecified
permissions are omitted (none). Use 'scion project github permissions reset'
to return to defaults.

Examples:
  scion project github permissions set contents=write pull_requests=write metadata=read
  scion project github permissions set contents=read metadata=read issues=none`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		perms, err := parseGitHubTokenPermissionsArgs(args)
		if err != nil {
			return err
		}
		client, projectID, err := resolveProjectForGitHub()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		return runProjectGitHubPermissionsSet(ctx, client.Projects(), projectID, perms, cmd.OutOrStdout())
	},
}

var projectGitHubPermissionsResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Reset GitHub App token permissions to defaults",
	Long: `Reset the project's GitHub App token permissions back to default mode
(default permissions capped to the App's granted permissions).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, projectID, err := resolveProjectForGitHub()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		return runProjectGitHubPermissionsReset(ctx, client.Projects(), projectID, cmd.OutOrStdout())
	},
}

func init() {
	projectGitHubPermissionsCmd.AddCommand(projectGitHubPermissionsSetCmd)
	projectGitHubPermissionsCmd.AddCommand(projectGitHubPermissionsResetCmd)
}

// parseGitHubTokenPermissionsArgs parses key=value pairs into a full-replacement
// GitHubTokenPermissions struct using the same validation rules as the Hub API.
func parseGitHubTokenPermissionsArgs(args []string) (hubclient.GitHubTokenPermissions, error) {
	if len(args) == 0 {
		return hubclient.GitHubTokenPermissions{}, fmt.Errorf("at least one permission must be specified (e.g., contents=write metadata=read)")
	}
	var perms hubclient.GitHubTokenPermissions
	seen := make(map[string]bool, len(args))
	for _, raw := range args {
		key, val, ok := strings.Cut(raw, "=")
		if !ok || key == "" {
			return hubclient.GitHubTokenPermissions{}, fmt.Errorf("invalid permission %q: expected key=level (e.g., contents=write)", raw)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if seen[key] {
			return hubclient.GitHubTokenPermissions{}, fmt.Errorf("duplicate permission %q", key)
		}
		seen[key] = true

		normalized := val
		if normalized == "none" {
			normalized = ""
		}
		if normalized != "" && normalized != "read" && normalized != "write" {
			return hubclient.GitHubTokenPermissions{}, fmt.Errorf("invalid level %q for %s: must be \"none\", \"read\", or \"write\"", val, key)
		}

		switch key {
		case "contents":
			perms.Contents = normalized
		case "pull_requests":
			perms.PullRequests = normalized
		case "issues":
			perms.Issues = normalized
		case "metadata":
			perms.Metadata = normalized
		case "checks":
			perms.Checks = normalized
		case "actions":
			perms.Actions = normalized
		default:
			return hubclient.GitHubTokenPermissions{}, fmt.Errorf("unknown permission %q: valid keys are contents, pull_requests, issues, metadata, checks, actions", key)
		}
	}

	if perms == (hubclient.GitHubTokenPermissions{}) {
		return hubclient.GitHubTokenPermissions{}, fmt.Errorf("at least one permission must be set to \"read\" or \"write\"; use 'scion project github permissions reset' to reset to defaults")
	}
	return perms, nil
}

func runProjectGitHubPermissionsShow(ctx context.Context, projects hubclient.ProjectService, projectID string, out io.Writer) error {
	info, err := projects.GetGitHub(ctx, projectID)
	if err != nil {
		return wrapHubError(err)
	}
	if isJSONOutput() {
		return outputJSON(info.Permissions)
	}
	return renderProjectGitHubPermissionsTable(out, info.Permissions)
}

func runProjectGitHubPermissionsSet(ctx context.Context, projects hubclient.ProjectService, projectID string, perms hubclient.GitHubTokenPermissions, out io.Writer) error {
	updated, err := projects.SetGitHubPermissions(ctx, projectID, perms)
	if err != nil {
		return wrapHubError(err)
	}
	if isJSONOutput() {
		return outputJSON(updated)
	}
	_, _ = fmt.Fprintf(out, "Updated custom GitHub token permissions for project %s.\n\n", stripControlChars(projectID))
	if info, getErr := projects.GetGitHub(ctx, projectID); getErr == nil && info != nil {
		return renderProjectGitHubPermissionsTable(out, info.Permissions)
	}
	return nil
}

func runProjectGitHubPermissionsReset(ctx context.Context, projects hubclient.ProjectService, projectID string, out io.Writer) error {
	if err := projects.ResetGitHubPermissions(ctx, projectID); err != nil {
		return wrapHubError(err)
	}
	if isJSONOutput() {
		return outputJSON(map[string]string{
			"project_id": projectID,
			"mode":       "default",
		})
	}
	_, _ = fmt.Fprintf(out, "Reset GitHub token permissions to defaults for project %s.\n\n", stripControlChars(projectID))
	if info, getErr := projects.GetGitHub(ctx, projectID); getErr == nil && info != nil {
		return renderProjectGitHubPermissionsTable(out, info.Permissions)
	}
	return nil
}
