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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/clitime"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/spf13/cobra"
)

// errUsageBudgetsExperimentDisabled is returned when the Hub responds with 404
// "route not found" (or any 404 on /api/v1/me/usage/budgets), indicating that
// tokenomics.token_budgets (or tokenomics.usage_ledger) is disabled.
var errUsageBudgetsExperimentDisabled = errors.New("token budgets are not enabled on this Hub (requires experiments tokenomics.usage_ledger and tokenomics.token_budgets)")

type usageBudgetsFlags struct {
	user  string
	group string
	json  bool
}

type usageBudgetsOverrideFlags struct {
	until  string
	clear  bool
	user   string
	reason string
	json   bool
}

func newUsageBudgetsCmd() *cobra.Command {
	var f usageBudgetsFlags
	cmd := &cobra.Command{
		Use:   "budgets",
		Short: "Show active token budgets and enforcement state",
		Long: `Show active raw-token budgets, consumption, enforcement state, and overrides.

By default, scopes to the current project when invoked inside one (or the
calling agent's own project in agent mode), and otherwise to your own user
budgets (/api/v1/me/usage/budgets).

Requires both the tokenomics.usage_ledger and tokenomics.token_budgets
experiments on the Hub.

Examples:
  scion usage budgets
  scion usage budgets --project my-proj
  scion usage budgets --user me
  scion usage budgets --user <user-id>
  scion usage budgets --group platform
  scion usage budgets --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUsageBudgets(cmd, f)
		},
	}

	cmd.Flags().StringVar(&f.user, "user", "", `Query budgets for a user ID (or "me" for the current user)`)
	cmd.Flags().StringVar(&f.group, "group", "", "Query pooled budgets for a group ID or slug")
	cmd.Flags().BoolVar(&f.json, "json", false, "Output raw JSON response from the Hub")

	markFlagsForMode(cmd, ModeHuman, "user", "group")
	cmd.AddCommand(newUsageBudgetsOverrideCmd())
	return cmd
}

func newUsageBudgetsOverrideCmd() *cobra.Command {
	var f usageBudgetsOverrideFlags
	cmd := &cobra.Command{
		Use:   "override <budget-id>",
		Short: "Temporarily override or clear a token budget breach",
		Long: `Set or clear a temporary override on an active token budget state row.

Reads the current budget state_version before submitting the override and
automatically retries once on a 412 Precondition Failed conflict.

By default, targets the current project (or --project). Pass --user <id> to
override a user or user_project budget (hub admin only).

Examples:
  scion usage budgets override <budget-id> --until 2h
  scion usage budgets override <budget-id> --until 1d --reason "release crunch"
  scion usage budgets override <budget-id> --until 2026-11-28T23:59:59Z --project my-proj
  scion usage budgets override <budget-id> --clear --project my-proj
  scion usage budgets override <budget-id> --until 4h --user <user-id>`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUsageBudgetsOverride(cmd, args[0], f)
		},
	}

	cmd.Flags().StringVar(&f.until, "until", "", "Override expiry (RFC 3339 timestamp or positive duration such as 30m, 2h, 1d)")
	cmd.Flags().BoolVar(&f.clear, "clear", false, "Clear an active override (sets override_until to null)")
	cmd.Flags().StringVar(&f.user, "user", "", `Target user ID (or "me") for user-scoped budget override (hub admin only)`)
	cmd.Flags().StringVar(&f.reason, "reason", "", "Optional audit reason recorded with the override")
	cmd.Flags().BoolVar(&f.json, "json", false, "Output raw JSON response from the Hub")

	return cmd
}

func runUsageBudgets(cmd *cobra.Command, f usageBudgetsFlags) error {
	mode := resolveMode()
	if err := checkFlagsForMode(cmd, mode); err != nil {
		return err
	}

	explicitProject := (projectPath != "" && projectSource != targetSourceContext) || globalMode
	userRef := strings.TrimSpace(f.user)
	groupRef := strings.TrimSpace(f.group)

	scopeCount := 0
	if explicitProject {
		scopeCount++
	}
	if userRef != "" {
		scopeCount++
	}
	if groupRef != "" {
		scopeCount++
	}
	if scopeCount > 1 {
		return newUsageError("at most one of --project/--global, --user, or --group may be specified")
	}

	client, uat, err := usageLoadHubClient()
	if err != nil {
		return err
	}

	parentCtx := cmd.Context()
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
	defer cancel()

	wantJSON := f.json || isJSONOutput()

	if groupRef != "" {
		resp, err := client.Usage().GetGroupBudgets(ctx, groupRef, nil)
		if err != nil {
			return formatUsageBudgetAPIError(err, true, false)
		}
		if wantJSON {
			return writeUsageRawJSON(cmd.OutOrStdout(), resp.Raw, resp)
		}
		writeGroupUsageBudgetsTable(cmd.OutOrStdout(), resp)
		return nil
	}

	var (
		resp         *hubclient.UsageBudgetsResponse
		entityScoped bool
	)
	switch {
	case userRef != "":
		if strings.EqualFold(userRef, "me") {
			resp, err = client.Usage().GetMeBudgets(ctx)
		} else {
			entityScoped = true
			resp, err = client.Usage().GetUserBudgets(ctx, userRef)
		}
	case explicitProject:
		res, resolveErr := usageResolveProject(ctx, client, uat, true)
		if resolveErr != nil {
			return resolveErr
		}
		if res.UnlinkedDir != "" {
			return unlinkedScopeProjectError(res.UnlinkedDir)
		}
		if res.ID == "" {
			return errCannotInferProject
		}
		entityScoped = true
		resp, err = client.Usage().GetProjectBudgets(ctx, res.ID)
	case mode == ModeAgent:
		res, resolveErr := usageResolveProject(ctx, client, uat, true)
		if resolveErr != nil {
			return resolveErr
		}
		if res.ID == "" {
			return errors.New("cannot determine the agent's project")
		}
		entityScoped = true
		resp, err = client.Usage().GetProjectBudgets(ctx, res.ID)
	default:
		res, resolveErr := usageResolveProject(ctx, client, uat, false)
		if resolveErr != nil {
			return resolveErr
		}
		if res.ID != "" {
			entityScoped = true
			resp, err = client.Usage().GetProjectBudgets(ctx, res.ID)
		} else {
			if res.UnlinkedDir != "" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Note: directory project %s is not linked to a hub; showing your user budgets (/me) instead.\n", res.UnlinkedDir)
			}
			resp, err = client.Usage().GetMeBudgets(ctx)
		}
	}
	if err != nil {
		return formatUsageBudgetAPIError(err, entityScoped, false)
	}

	if wantJSON {
		return writeUsageRawJSON(cmd.OutOrStdout(), resp.Raw, resp)
	}
	writeUsageBudgetsTable(cmd.OutOrStdout(), resp)
	return nil
}

func runUsageBudgetsOverride(cmd *cobra.Command, rawBudgetID string, f usageBudgetsOverrideFlags) error {
	budgetID := strings.TrimSpace(rawBudgetID)
	if budgetID == "" {
		return newUsageError("budget ID is required")
	}

	untilRaw := strings.TrimSpace(f.until)
	if untilRaw == "" && !f.clear {
		return newUsageError("one of --until or --clear is required")
	}
	if untilRaw != "" && f.clear {
		return newUsageError("--until and --clear are mutually exclusive")
	}

	var overrideUntil *time.Time
	if untilRaw != "" {
		parsed, err := parseBudgetOverrideUntil(untilRaw, clitime.Now())
		if err != nil {
			return newUsageError("%s", err.Error())
		}
		overrideUntil = &parsed
	}

	explicitProject := (projectPath != "" && projectSource != targetSourceContext) || globalMode
	userRef := strings.TrimSpace(f.user)
	if explicitProject && userRef != "" {
		return newUsageError("at most one of --project/--global or --user may be specified")
	}

	client, uat, err := usageLoadHubClient()
	if err != nil {
		return err
	}

	parentCtx := cmd.Context()
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
	defer cancel()

	var (
		scopeDesc    string
		entityScoped bool
		fetchBudgets func(context.Context) (*hubclient.UsageBudgetsResponse, error)
		apply        func(context.Context, string, *hubclient.OverrideUsageBudgetRequest) (*hubclient.UsageBudgetEntry, error)
	)

	if userRef != "" {
		if strings.EqualFold(userRef, "me") {
			scopeDesc = "user (me)"
			entityScoped = false
			fetchBudgets = func(ctx context.Context) (*hubclient.UsageBudgetsResponse, error) {
				return client.Usage().GetMeBudgets(ctx)
			}
			apply = func(ctx context.Context, targetID string, req *hubclient.OverrideUsageBudgetRequest) (*hubclient.UsageBudgetEntry, error) {
				return client.Usage().OverrideUserBudget(ctx, targetID, budgetID, req)
			}
		} else {
			scopeDesc = fmt.Sprintf("user (%s)", userRef)
			entityScoped = true
			fetchBudgets = func(ctx context.Context) (*hubclient.UsageBudgetsResponse, error) {
				return client.Usage().GetUserBudgets(ctx, userRef)
			}
			apply = func(ctx context.Context, _ string, req *hubclient.OverrideUsageBudgetRequest) (*hubclient.UsageBudgetEntry, error) {
				return client.Usage().OverrideUserBudget(ctx, userRef, budgetID, req)
			}
		}
	} else {
		res, resolveErr := usageResolveProject(ctx, client, uat, true)
		if resolveErr != nil {
			return resolveErr
		}
		if res.UnlinkedDir != "" {
			return unlinkedScopeProjectError(res.UnlinkedDir)
		}
		if res.ID == "" {
			return errCannotInferProject
		}
		projectID := res.ID
		scopeDesc = fmt.Sprintf("project (%s)", projectID)
		entityScoped = true
		fetchBudgets = func(ctx context.Context) (*hubclient.UsageBudgetsResponse, error) {
			return client.Usage().GetProjectBudgets(ctx, projectID)
		}
		apply = func(ctx context.Context, _ string, req *hubclient.OverrideUsageBudgetRequest) (*hubclient.UsageBudgetEntry, error) {
			return client.Usage().OverrideProjectBudget(ctx, projectID, budgetID, req)
		}
	}

	reason := strings.TrimSpace(f.reason)
	var updated *hubclient.UsageBudgetEntry
	for attempt := 0; attempt < 2; attempt++ {
		budgetsResp, fetchErr := fetchBudgets(ctx)
		if fetchErr != nil {
			return formatUsageBudgetAPIError(fetchErr, entityScoped, false)
		}

		var match *hubclient.UsageBudgetEntry
		for i := range budgetsResp.Budgets {
			if budgetsResp.Budgets[i].ID == budgetID {
				match = &budgetsResp.Budgets[i]
				break
			}
		}
		if match == nil {
			if attempt > 0 {
				return fmt.Errorf("budget %q is no longer present in %s after retry", budgetID, scopeDesc)
			}
			return fmt.Errorf("budget %q not found in active %s budgets (only budgets with an active state row can be overridden)", budgetID, scopeDesc)
		}
		if match.StateVersion <= 0 {
			return fmt.Errorf("budget %q has no valid state_version (%d); cannot submit override", budgetID, match.StateVersion)
		}

		ver := match.StateVersion
		req := &hubclient.OverrideUsageBudgetRequest{
			OverrideUntil: overrideUntil,
			StateVersion:  &ver,
			Reason:        reason,
		}
		targetScopeID := budgetsResp.Scope.ID
		entry, ovErr := apply(ctx, targetScopeID, req)
		if ovErr == nil {
			updated = entry
			break
		}
		if isBudgetAPIStatus(ovErr, http.StatusPreconditionFailed) {
			if attempt == 0 {
				continue
			}
			return fmt.Errorf("budget %s was modified concurrently (412 Precondition Failed after retry); re-run the command to try again", budgetID)
		}
		return formatUsageBudgetAPIError(ovErr, true, true)
	}

	if f.json || isJSONOutput() {
		return writeUsageRawJSON(cmd.OutOrStdout(), updated.Raw, updated)
	}
	writeUsageBudgetOverrideResult(cmd.OutOrStdout(), updated, f.clear)
	return nil
}

// parseBudgetOverrideUntil parses an RFC 3339 timestamp or a positive duration
// (standard Go duration such as "30m", "2h", "1h30m", or day-suffixed duration
// such as "1d", "2d12h") relative to now.
func parseBudgetOverrideUntil(raw string, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, errors.New("--until value must not be empty")
	}
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts.UTC(), nil
	}
	d, err := parseBudgetDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --until %q: expected an RFC 3339 timestamp (e.g. 2026-11-28T23:59:59Z) or positive duration (e.g. 30m, 2h, 1d)", raw)
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("--until duration %q must be positive", raw)
	}
	return now.UTC().Add(d), nil
}

// parseBudgetDuration parses Go time.ParseDuration values and additionally
// supports leading integer day units ("1d", "2d12h", "1d30m").
func parseBudgetDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("empty duration")
	}
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		return time.ParseDuration(s)
	}
	if idx := strings.IndexByte(s, 'd'); idx >= 0 {
		daysStr := s[:idx]
		if daysStr == "" {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		days, err := strconv.ParseInt(daysStr, 10, 64)
		if err != nil || days < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		total := time.Duration(days) * 24 * time.Hour
		rest := s[idx+1:]
		if rest != "" {
			sub, err := time.ParseDuration(rest)
			if err != nil || sub < 0 {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			total += sub
		}
		return total, nil
	}
	return time.ParseDuration(s)
}

func isBudgetAPIStatus(err error, status int) bool {
	var apiErr *apiclient.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

func formatUsageBudgetAPIError(err error, entityScoped, isOverride bool) error {
	var apiErr *apiclient.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.StatusCode {
	case http.StatusNotFound:
		if !entityScoped || strings.EqualFold(strings.TrimSpace(apiErr.Message), "route not found") {
			return errUsageBudgetsExperimentDisabled
		}
		return fmt.Errorf("%w (if the target exists, verify experiments tokenomics.usage_ledger and tokenomics.token_budgets are enabled on this Hub)", err)
	case http.StatusForbidden:
		if isOverride {
			return fmt.Errorf("permission denied overriding budget (requires project admin for project budgets or hub admin for user budgets): %w", err)
		}
		return fmt.Errorf("permission denied reading token budgets: %w", err)
	case http.StatusPreconditionFailed:
		return fmt.Errorf("budget state changed concurrently (412 Precondition Failed; state_version mismatch): %w", err)
	case http.StatusPreconditionRequired:
		return fmt.Errorf("budget override requires state_version (428 Precondition Required): %w", err)
	default:
		return err
	}
}

func writeUsageBudgetsTable(out io.Writer, resp *hubclient.UsageBudgetsResponse) {
	if resp == nil {
		return
	}

	scopeLabel := resp.Scope.Kind
	if resp.Scope.ID != "" {
		scopeLabel = fmt.Sprintf("%s (%s)", resp.Scope.Kind, resp.Scope.ID)
	}
	_, _ = fmt.Fprintf(out, "Scope:   %s\n", scopeLabel)
	_, _ = fmt.Fprintf(out, "As of:   %s\n", clitime.Format(resp.AsOf, clitime.Minute))
	_, _ = fmt.Fprintln(out)

	if len(resp.Budgets) == 0 {
		_, _ = fmt.Fprintln(out, "No active token budgets.")
		return
	}

	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tLIMIT\tSCOPE\tWINDOW\tCONSUMED / CAP\tPERCENT\tSTATE\tACTION\tOVERRIDE UNTIL")
	for _, b := range resp.Budgets {
		writeBudgetEntryRow(tw, b.ID, b.LimitName, b.ScopeKind, b.ScopeID, b.ProjectScopeID, b.WindowStartUTC, b.WindowEndUTC, b.Consumed, b.Cap, b.State, b.TokenAction, b.OverrideUntil)
	}
	_ = tw.Flush()
}

func writeGroupUsageBudgetsTable(out io.Writer, resp *hubclient.GroupUsageBudgetsResponse) {
	if resp == nil {
		return
	}

	scopeLabel := resp.Scope.Kind
	if resp.Scope.ID != "" {
		scopeLabel = fmt.Sprintf("%s (%s)", resp.Scope.Kind, resp.Scope.ID)
	}
	_, _ = fmt.Fprintf(out, "Scope:   %s\n", scopeLabel)
	if resp.GroupName != "" {
		_, _ = fmt.Fprintf(out, "Group:   %s\n", resp.GroupName)
	}
	_, _ = fmt.Fprintf(out, "As of:   %s\n", clitime.Format(resp.AsOf, clitime.Minute))
	_, _ = fmt.Fprintln(out)

	if len(resp.Budgets) == 0 {
		_, _ = fmt.Fprintln(out, "No active token budgets.")
	} else {
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tLIMIT\tSCOPE\tWINDOW\tCONSUMED / CAP\tPERCENT\tSTATE\tACTION\tOVERRIDE UNTIL")
		for _, b := range resp.Budgets {
			writeBudgetEntryRow(tw, b.ID, b.LimitName, b.ScopeKind, b.ScopeID, b.ProjectScopeID, b.WindowStartUTC, b.WindowEndUTC, b.Consumed, b.Cap, b.State, b.TokenAction, b.OverrideUntil)
		}
		_ = tw.Flush()

		for _, b := range resp.Budgets {
			if len(b.Members) == 0 {
				continue
			}
			_, _ = fmt.Fprintln(out)
			_, _ = fmt.Fprintf(out, "Members (%s, %s):\n", b.LimitName, formatBudgetScope(b.ScopeKind, b.ScopeID, b.ProjectScopeID))
			mtw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(mtw, "MEMBER\tTOKENS\tCALLS\tEVENTS")
			for _, m := range b.Members {
				_, _ = fmt.Fprintf(mtw, "%s\t%s\t%s\t%s\n",
					formatGroupBudgetMember(m),
					formatCommaInt(m.TokensSummable),
					formatCommaInt(m.Calls),
					formatCommaInt(m.Events),
				)
			}
			_ = mtw.Flush()
		}
	}

	if resp.NonAdditive {
		_, _ = fmt.Fprintln(out)
		_, _ = fmt.Fprintln(out, "Note: pooled group budgets are non-additive across overlapping groups; spend by members in multiple budgeted groups is charged in full to each group.")
	}
}

func writeUsageBudgetOverrideResult(out io.Writer, entry *hubclient.UsageBudgetEntry, cleared bool) {
	if entry == nil {
		return
	}
	if cleared {
		_, _ = fmt.Fprintf(out, "Cleared override on budget %s (%s).\n", entry.ID, entry.LimitName)
	} else if entry.OverrideUntil != nil {
		_, _ = fmt.Fprintf(out, "Set override on budget %s (%s) until %s.\n", entry.ID, entry.LimitName, clitime.Format(*entry.OverrideUntil, clitime.Minute))
	} else {
		_, _ = fmt.Fprintf(out, "Updated budget %s (%s).\n", entry.ID, entry.LimitName)
	}
	_, _ = fmt.Fprintf(out, "State:         %s\n", formatBudgetText(entry.State))
	_, _ = fmt.Fprintf(out, "Consumed/Cap:  %s\n", formatBudgetConsumedCap(entry.Consumed, entry.Cap))
	_, _ = fmt.Fprintf(out, "State version: %d\n", entry.StateVersion)
}

func writeBudgetEntryRow(
	tw *tabwriter.Writer,
	id, limitName, scopeKind, scopeID, projectScopeID string,
	winStart, winEnd time.Time,
	consumed int64,
	capPtr *int64,
	state, action string,
	overrideUntil *time.Time,
) {
	overrideCell := usageUnknownCell
	if overrideUntil != nil && !overrideUntil.IsZero() {
		overrideCell = clitime.Format(*overrideUntil, clitime.Minute)
	}
	_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		formatBudgetText(id),
		formatBudgetText(limitName),
		formatBudgetScope(scopeKind, scopeID, projectScopeID),
		formatBudgetWindow(winStart, winEnd),
		formatBudgetConsumedCap(consumed, capPtr),
		formatBudgetPercent(consumed, capPtr),
		formatBudgetText(state),
		formatBudgetText(action),
		overrideCell,
	)
}

func formatBudgetText(s string) string {
	if strings.TrimSpace(s) == "" {
		return usageUnknownCell
	}
	return s
}

func formatBudgetScope(scopeKind, scopeID, projectScopeID string) string {
	if scopeKind == "" {
		return usageUnknownCell
	}
	if projectScopeID != "" {
		return fmt.Sprintf("%s:%s (%s)", scopeKind, scopeID, projectScopeID)
	}
	if scopeID != "" {
		return fmt.Sprintf("%s:%s", scopeKind, scopeID)
	}
	return scopeKind
}

func formatBudgetWindow(start, end time.Time) string {
	if start.IsZero() && end.IsZero() {
		return usageUnknownCell
	}
	if !start.IsZero() && !end.IsZero() {
		if end.Sub(start) == 24*time.Hour {
			return fmt.Sprintf("day (ends %s)", clitime.Format(end, clitime.Minute))
		}
		su := start.UTC()
		if su.Day() == 1 && su.Hour() == 0 && su.Minute() == 0 && su.Second() == 0 && end.UTC().Equal(su.AddDate(0, 1, 0)) {
			return fmt.Sprintf("month (ends %s)", clitime.Format(end, clitime.Minute))
		}
	}
	return fmt.Sprintf("%s .. %s", clitime.Format(start, clitime.Minute), clitime.Format(end, clitime.Minute))
}

func formatBudgetConsumedCap(consumed int64, capPtr *int64) string {
	if capPtr == nil || *capPtr <= 0 {
		return fmt.Sprintf("%s / unlimited", formatCommaInt(consumed))
	}
	return fmt.Sprintf("%s / %s", formatCommaInt(consumed), formatCommaInt(*capPtr))
}

func formatBudgetPercent(consumed int64, capPtr *int64) string {
	if capPtr == nil || *capPtr <= 0 {
		return usageUnknownCell
	}
	pct := (float64(consumed) / float64(*capPtr)) * 100.0
	return fmt.Sprintf("%.1f%%", pct)
}

func formatGroupBudgetMember(m hubclient.GroupBudgetMemberView) string {
	switch {
	case m.DisplayName != "" && m.Email != "":
		return fmt.Sprintf("%s <%s>", m.DisplayName, m.Email)
	case m.Email != "":
		return m.Email
	case m.DisplayName != "":
		return m.DisplayName
	case m.UserID != "":
		return m.UserID
	default:
		return usageUnknownCell
	}
}
