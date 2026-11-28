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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	convDeleteYes  bool
	convDeleteJSON bool

	// conversationDeleteIsTerminal reports whether stdin is an interactive TTY.
	// Tests override this hook to exercise TTY and non-TTY confirmation paths.
	conversationDeleteIsTerminal = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd()))
	}
)

var conversationDeleteCmd = &cobra.Command{
	Use:   "delete <conversation-ref>...",
	Short: "Delete one or more conversations",
	Long: `Soft-delete one or more native direct-message conversations for both participants.

Conversation references:
  conv:<uuid>     Direct conversation ID (full 36-character UUID)
  <uuid>          Bare 36-character conversation ID

Requires confirmation on an interactive terminal unless --yes is passed.
Running non-interactively without --yes returns an error.

Examples:
  scion conversation delete conv:a1b2c3d4-...
  scion conversation delete a1b2c3d4-... --yes
  scion conversation delete conv:a1b2c3d4-... --yes --json`,
	Args: cobra.MinimumNArgs(1),
	RunE: runConversationDelete,
}

func init() {
	conversationCmd.AddCommand(conversationDeleteCmd)
	conversationDeleteCmd.Flags().BoolVarP(&convDeleteYes, "yes", "y", false, "Skip confirmation prompt")
	conversationDeleteCmd.Flags().BoolVar(&convDeleteJSON, "json", false, "Output in JSON format")
}

type deleteTarget struct {
	ref   string
	id    string
	label string
}

type conversationDeleteJSONResult struct {
	Deleted []string `json:"deleted"`
}

func runConversationDelete(cmd *cobra.Command, args []string) error {
	if convDeleteJSON {
		outputFormat = "json"
	}

	hubCtx, client, err := requireHubClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmdContext(cmd), 30*time.Second)
	defer cancel()

	projectID := ""
	if hubCtx != nil {
		projectID = hubCtx.ProjectID
	}
	return executeConversationDelete(ctx, cmd, client, projectID, args, convDeleteYes)
}

func executeConversationDelete(ctx context.Context, cmd *cobra.Command, client hubclient.Client, projectID string, refs []string, yes bool) error {
	targets := make([]deleteTarget, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		id, err := resolveConversationRef(ctx, client, projectID, ref)
		if err != nil {
			return err
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		detail, err := client.Conversations().Get(ctx, id)
		if err != nil {
			return fmt.Errorf("failed to get conversation %s: %w", ref, err)
		}
		targets = append(targets, deleteTarget{
			ref:   ref,
			id:    id,
			label: conversationDeleteLabel(detail),
		})
	}

	if !yes {
		if !conversationDeleteIsTerminal() {
			return errors.New("confirmation required: pass --yes to delete conversations non-interactively")
		}
		errOut := cmd.ErrOrStderr()
		if len(targets) == 1 {
			if targets[0].label != "" && targets[0].label != targets[0].id {
				_, _ = fmt.Fprintf(errOut, "Delete conversation %s (%s) for both participants? [y/N]: ", targets[0].label, targets[0].id)
			} else {
				_, _ = fmt.Fprintf(errOut, "Delete conversation %s for both participants? [y/N]: ", targets[0].id)
			}
		} else {
			_, _ = fmt.Fprintf(errOut, "Conversations to delete for both participants:\n")
			for _, t := range targets {
				if t.label != "" && t.label != t.id {
					_, _ = fmt.Fprintf(errOut, "  - %s (%s)\n", t.label, t.id)
				} else {
					_, _ = fmt.Fprintf(errOut, "  - %s\n", t.id)
				}
			}
			_, _ = fmt.Fprintf(errOut, "Delete %d conversations for both participants? [y/N]: ", len(targets))
		}
		reader := bufio.NewReader(cmd.InOrStdin())
		line, readErr := reader.ReadString('\n')
		if readErr != nil && len(line) == 0 {
			return fmt.Errorf("failed to read confirmation: %w", readErr)
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			_, _ = fmt.Fprintln(errOut, "Cancelled.")
			return nil
		}
	}

	deleted := make([]string, 0, len(targets))
	var deleteErr error
	for _, t := range targets {
		if err := client.Conversations().Delete(ctx, t.id); err != nil {
			if apiclient.IsNotFoundError(err) {
				deleteErr = fmt.Errorf("conversation %s not found, or conversation lifecycle is not enabled on this hub (experiment hub.conversation_lifecycle)", t.id)
			} else {
				deleteErr = fmt.Errorf("failed to delete conversation %s: %w", t.id, err)
			}
			break
		}
		deleted = append(deleted, t.id)
	}

	if len(deleted) > 0 {
		if isJSONOutput() {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(conversationDeleteJSONResult{Deleted: deleted}); err != nil {
				return err
			}
		} else {
			out := cmd.OutOrStdout()
			for _, id := range deleted {
				_, _ = fmt.Fprintf(out, "Deleted conversation %s\n", id)
			}
		}
	}
	return deleteErr
}

func conversationDeleteLabel(detail *hubclient.ConversationDetail) string {
	if detail == nil {
		return ""
	}
	if detail.DMPeer != nil {
		if detail.DMPeer.Name != "" {
			return "DM with " + detail.DMPeer.Name
		}
		if detail.DMPeer.ID != "" {
			return "DM with " + detail.DMPeer.ID
		}
	}
	if detail.ThreadName != "" {
		return "#" + detail.ThreadName
	}
	if detail.DisplayName != "" {
		return detail.DisplayName
	}
	return detail.ID
}
