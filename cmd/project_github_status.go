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
	"text/tabwriter"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/spf13/cobra"
)

var projectGitHubStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the project's GitHub App association and effective token permissions",
	Long: `Display the project's GitHub App installation association, health status,
and effective token permissions (requested, App grant, effective, and any cap reasons).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, projectID, err := resolveProjectForGitHub()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		return runProjectGitHubStatus(ctx, client.Projects(), projectID, cmd.OutOrStdout())
	},
}

var projectGitHubPermRows = []struct {
	key   string
	label string
}{
	{"contents", "Contents"},
	{"pull_requests", "Pull requests"},
	{"issues", "Issues"},
	{"checks", "Checks"},
	{"actions", "Actions"},
	{"metadata", "Metadata"},
}

func runProjectGitHubStatus(ctx context.Context, projects hubclient.ProjectService, projectID string, out io.Writer) error {
	info, err := projects.GetGitHub(ctx, projectID)
	if err != nil {
		return wrapHubError(err)
	}

	if isJSONOutput() {
		return outputJSON(info)
	}

	if info.Remote.Owner != "" && info.Remote.Repo != "" {
		_, _ = fmt.Fprintf(out, "Repository:    %s/%s", stripControlChars(info.Remote.Owner), stripControlChars(info.Remote.Repo))
		if info.App.WebHost != "" {
			_, _ = fmt.Fprintf(out, " (%s)", info.App.WebHost)
		}
		_, _ = fmt.Fprintln(out)
	} else {
		_, _ = fmt.Fprintln(out, "Repository:    (none on configured GitHub host)")
	}

	if info.App.Configured {
		if info.App.Slug != "" {
			_, _ = fmt.Fprintf(out, "GitHub App:    @%s (configured)\n", stripControlChars(info.App.Slug))
		} else {
			_, _ = fmt.Fprintln(out, "GitHub App:    configured")
		}
	} else {
		_, _ = fmt.Fprintln(out, "GitHub App:    not configured")
	}

	if info.Association.InstallationID == nil {
		_, _ = fmt.Fprintln(out, "Association:   not connected")
	} else {
		acct := stripControlChars(info.Association.AccountLogin)
		if acct != "" {
			_, _ = fmt.Fprintf(out, "Installation:  %s (#%d)\n", acct, *info.Association.InstallationID)
		} else {
			_, _ = fmt.Fprintf(out, "Installation:  #%d\n", *info.Association.InstallationID)
		}
		if info.Association.Authorized {
			basis := ""
			if info.Association.Basis != nil {
				basis = *info.Association.Basis
			}
			switch basis {
			case "admin_binding":
				binder := stripControlChars(info.Association.BoundByName)
				if binder == "" {
					binder = stripControlChars(info.Association.BoundBy)
				}
				if binder != "" && info.Association.BoundAt != "" {
					_, _ = fmt.Fprintf(out, "Authorized by: admin_binding (%s, %s)\n", binder, info.Association.BoundAt)
				} else if binder != "" {
					_, _ = fmt.Fprintf(out, "Authorized by: admin_binding (%s)\n", binder)
				} else {
					_, _ = fmt.Fprintln(out, "Authorized by: admin_binding")
				}
			case "creator_push":
				_, _ = fmt.Fprintln(out, "Authorized by: creator_push (write allowed)")
			case "creator_pull":
				_, _ = fmt.Fprintln(out, "Authorized by: creator_pull (read-only)")
			default:
				_, _ = fmt.Fprintf(out, "Authorized by: %s\n", stripControlChars(basis))
			}
		} else if !info.Association.Verified {
			_, _ = fmt.Fprintf(out, "Authorized:    unverified (%s)\n", stripControlChars(info.Association.Reason))
		} else {
			_, _ = fmt.Fprintf(out, "Authorized:    no (%s)\n", stripControlChars(info.Association.Reason))
		}
	}

	if info.Status != nil && info.Status.State != "" {
		_, _ = fmt.Fprintf(out, "Status:        %s", stripControlChars(info.Status.State))
		if info.Status.ErrorMessage != "" {
			_, _ = fmt.Fprintf(out, " (%s)", stripControlChars(info.Status.ErrorMessage))
		}
		_, _ = fmt.Fprintln(out)
	}

	_, _ = fmt.Fprintln(out)
	return renderProjectGitHubPermissionsTable(out, info.Permissions)
}

func renderProjectGitHubPermissionsTable(out io.Writer, perms hubclient.ProjectGitHubPermissions) error {
	_, _ = fmt.Fprintf(out, "Token Permissions (mode: %s)\n", perms.Mode)
	appGrantKnown := perms.AppGrantKnown || perms.AppGranted != nil
	if !perms.AppGrantKnown && perms.AppGranted == nil {
		_, _ = fmt.Fprintln(out, "Note: App permissions not yet loaded.")
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "PERMISSION\tREQUESTED\tAPP GRANT\tEFFECTIVE\tNOTE")
	for _, row := range projectGitHubPermRows {
		reqRaw := perms.Requested[row.key]
		appRaw := perms.AppGranted[row.key]
		effRaw := perms.Effective[row.key]
		req := dashIfEmpty(reqRaw)
		appGrant := dashIfEmpty(appRaw)
		eff := dashIfEmpty(effRaw)
		note := formatProjectGitHubPermNote(perms, row.key, reqRaw, appRaw, appGrantKnown)
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", row.label, req, appGrant, eff, note)
	}
	return tw.Flush()
}

func permLevelRank(level string) int {
	switch level {
	case "read":
		return 1
	case "write", "admin":
		return 2
	default:
		return 0
	}
}

func formatProjectGitHubPermNote(perms hubclient.ProjectGitHubPermissions, key, requested, appGranted string, appGrantKnown bool) string {
	capNote := formatCapReason(perms.CapReasons[key], requested)
	beyondNote := ""
	if perms.Mode == "custom" && appGrantKnown && requested != "" && permLevelRank(requested) > permLevelRank(appGranted) {
		beyondNote = "warning: the App does not grant this; tokens will fail until the App is updated"
	}
	switch {
	case capNote != "" && beyondNote != "":
		return capNote + "; " + beyondNote
	case capNote != "":
		return capNote
	default:
		return beyondNote
	}
}

func dashIfEmpty(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func formatCapReason(reason, requested string) string {
	switch reason {
	case "app_grant":
		if requested != "" {
			return fmt.Sprintf("app not granted %s", requested)
		}
		return "capped by app grant"
	case "association_read_only":
		return "association is read-only"
	default:
		return ""
	}
}
