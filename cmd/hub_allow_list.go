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
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/spf13/cobra"
)

var (
	allowListOutputJSON bool
	allowListAddNote    string
)

var hubAllowListCmd = &cobra.Command{
	Use:   "allow-list",
	Short: "Manage the user allow list",
	Long: `Manage the email allow list for invite-only access mode.

When user_access_mode is set to "invite_only", only emails on this
allow list (plus admin emails) are permitted to log in.

Examples:
  scion hub allow-list
  scion hub allow-list add alice@example.com --note "New hire"
  scion hub allow-list remove alice@example.com`,
	Args: cobra.NoArgs,
	RunE: runAllowListList,
}

var hubAllowListAddCmd = &cobra.Command{
	Use:   "add EMAIL",
	Short: "Add an email to the allow list",
	Long: `Add an email address to the allow list.

Examples:
  scion hub allow-list add alice@example.com
  scion hub allow-list add bob@example.com --note "Contractor, Q3"`,
	Args: cobra.ExactArgs(1),
	RunE: runAllowListAdd,
}

var hubAllowListRemoveCmd = &cobra.Command{
	Use:   "remove EMAIL",
	Short: "Remove an email from the allow list",
	Long: `Remove an email address from the allow list.

Examples:
  scion hub allow-list remove alice@example.com`,
	Args: cobra.ExactArgs(1),
	RunE: runAllowListRemove,
}

var hubAllowListListCmd = &cobra.Command{
	Use:   "list",
	Short: "List allow list entries",
	Long: `List all email addresses on the allow list.

Examples:
  scion hub allow-list list
  scion hub allow-list list --json`,
	Args: cobra.NoArgs,
	RunE: runAllowListList,
}

func init() {
	hubCmd.AddCommand(hubAllowListCmd)
	hubAllowListCmd.AddCommand(hubAllowListAddCmd)
	hubAllowListCmd.AddCommand(hubAllowListRemoveCmd)
	hubAllowListCmd.AddCommand(hubAllowListListCmd)

	hubAllowListAddCmd.Flags().StringVar(&allowListAddNote, "note", "", "Optional note for this entry")

	hubAllowListCmd.Flags().BoolVar(&allowListOutputJSON, "json", false, "Output in JSON format")
	hubAllowListListCmd.Flags().BoolVar(&allowListOutputJSON, "json", false, "Output in JSON format")
}

func runAllowListList(cmd *cobra.Command, args []string) error {
	resolvedPath, _, err := config.ResolveGrovePath(grovePath)
	if err != nil {
		return fmt.Errorf("failed to resolve grove path: %w", err)
	}

	settings, err := config.LoadSettings(resolvedPath)
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	client, err := getHubClient(settings)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.AllowList().List(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to list allow list: %w", err)
	}

	if allowListOutputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	if len(resp.Items) == 0 {
		fmt.Println("No entries in the allow list.")
		return nil
	}

	fmt.Printf("%-40s %-30s %s\n", "EMAIL", "ADDED", "NOTE")
	for _, entry := range resp.Items {
		fmt.Printf("%-40s %-30s %s\n",
			entry.Email,
			entry.Created.Format(time.RFC3339),
			entry.Note,
		)
	}
	fmt.Printf("\nTotal: %d entries\n", resp.TotalCount)

	return nil
}

func runAllowListAdd(cmd *cobra.Command, args []string) error {
	email := args[0]

	resolvedPath, _, err := config.ResolveGrovePath(grovePath)
	if err != nil {
		return fmt.Errorf("failed to resolve grove path: %w", err)
	}

	settings, err := config.LoadSettings(resolvedPath)
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	client, err := getHubClient(settings)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	entry, err := client.AllowList().Add(ctx, email, allowListAddNote)
	if err != nil {
		return fmt.Errorf("failed to add to allow list: %w", err)
	}

	if allowListOutputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entry)
	}

	fmt.Printf("Added %s to the allow list.\n", entry.Email)
	return nil
}

func runAllowListRemove(cmd *cobra.Command, args []string) error {
	email := args[0]

	resolvedPath, _, err := config.ResolveGrovePath(grovePath)
	if err != nil {
		return fmt.Errorf("failed to resolve grove path: %w", err)
	}

	settings, err := config.LoadSettings(resolvedPath)
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	client, err := getHubClient(settings)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.AllowList().Remove(ctx, email); err != nil {
		return fmt.Errorf("failed to remove from allow list: %w", err)
	}

	fmt.Printf("Removed %s from the allow list.\n", email)
	return nil
}
