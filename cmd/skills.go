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
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/spf13/cobra"
)

// skillsCmd represents the top-level skills command group.
var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Manage skill bank skills",
	Long: `List, publish, inspect, and manage skills in the Hub skill bank.

Skills are reusable instruction bundles (SKILL.md + optional scripts) that can be
attached to agent templates. The skill bank provides versioned, scoped storage
so that skills can be shared across projects and teams.`,
}

// --- skills list ---

var skillsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available skills",
	Long: `List skills from the Hub skill bank.

By default, lists all skills visible to the current user. Use --scope to filter
by scope (global, project, user) and --search for name/description search.

Examples:
  scion skills list
  scion skills list --scope project
  scion skills list --search "security"`,
	RunE: runSkillsList,
}

func runSkillsList(cmd *cobra.Command, args []string) error {
	scope, _ := cmd.Flags().GetString("scope")
	search, _ := cmd.Flags().GetString("search")

	hubCtx, err := CheckHubAvailabilityWithOptions(projectPath, true)
	if err != nil {
		return err
	}
	if hubCtx == nil {
		return fmt.Errorf("Hub integration is not enabled. Use 'scion hub enable' first")
	}

	PrintUsingHub(hubCtx.Endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := &hubclient.ListSkillsOptions{
		Search: search,
		Scope:  scope,
		Status: "active",
	}

	// For project-scoped listings, include the project ID.
	if scope == "project" {
		projectID, pidErr := GetProjectID(hubCtx)
		if pidErr == nil && projectID != "" {
			opts.ProjectID = projectID
		}
	}

	resp, err := hubCtx.Client.Skills().List(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to list skills: %w", err)
	}

	skills := resp.Skills
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})

	if isJSONOutput() {
		return outputJSON(map[string]interface{}{
			"skills": skills,
		})
	}

	if len(skills) == 0 {
		fmt.Println("No skills found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSCOPE\tVERSION\tSTATUS\tDESCRIPTION")
	for _, s := range skills {
		desc := s.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		version := s.LatestVersion
		if version == "" {
			version = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Scope, version, s.Status, desc)
	}
	w.Flush()
	return nil
}

// --- skills show ---

var skillsShowCmd = &cobra.Command{
	Use:   "show <name-or-id>",
	Short: "Show skill details",
	Long: `Show detailed information about a skill, including its latest version
and file manifest.

Examples:
  scion skills show security-audit
  scion skills show abc123-def456`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsShow,
}

func runSkillsShow(cmd *cobra.Command, args []string) error {
	nameOrID := args[0]

	hubCtx, err := CheckHubAvailabilityWithOptions(projectPath, true)
	if err != nil {
		return err
	}
	if hubCtx == nil {
		return fmt.Errorf("Hub integration is not enabled. Use 'scion hub enable' first")
	}

	PrintUsingHub(hubCtx.Endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Try to find the skill by name first, then by ID.
	skill, err := resolveSkillByNameOrID(ctx, hubCtx, nameOrID)
	if err != nil {
		return err
	}

	if isJSONOutput() {
		return outputJSON(skill)
	}

	fmt.Printf("Name:        %s\n", skill.Name)
	fmt.Printf("ID:          %s\n", skill.ID)
	if skill.DisplayName != "" {
		fmt.Printf("Display:     %s\n", skill.DisplayName)
	}
	if skill.Description != "" {
		fmt.Printf("Description: %s\n", skill.Description)
	}
	fmt.Printf("Scope:       %s\n", skill.Scope)
	if skill.ScopeID != "" {
		fmt.Printf("Scope ID:    %s\n", skill.ScopeID)
	}
	fmt.Printf("Status:      %s\n", skill.Status)
	if skill.LatestVersion != "" {
		fmt.Printf("Version:     %s\n", skill.LatestVersion)
	}
	fmt.Printf("Created:     %s\n", skill.Created.Format(time.RFC3339))
	fmt.Printf("Updated:     %s\n", skill.Updated.Format(time.RFC3339))

	return nil
}

// --- skills create ---

var skillsCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Scaffold a new skill directory",
	Long: `Create a new skill directory with a SKILL.md template.

This scaffolds a local skill directory that can later be published to the Hub.

Examples:
  scion skills create my-skill
  scion skills create security-audit`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsCreate,
}

func runSkillsCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	outputDir, _ := cmd.Flags().GetString("output")

	if outputDir == "" {
		outputDir = "."
	}

	skillDir := filepath.Join(outputDir, name)

	// Check if directory already exists
	if _, err := os.Stat(skillDir); err == nil {
		return fmt.Errorf("directory %s already exists", skillDir)
	}

	// Create directory
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write SKILL.md template
	skillMD := fmt.Sprintf(`---
name: %s
description: >-
  TODO: Describe what this skill does.
---

# %s

TODO: Write skill instructions here.
`, name, name)

	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0644); err != nil {
		return fmt.Errorf("failed to write SKILL.md: %w", err)
	}

	if isJSONOutput() {
		return outputJSON(ActionResult{
			Status:  "success",
			Command: "skills create",
			Message: fmt.Sprintf("Skill '%s' scaffolded at %s", name, skillDir),
			Details: map[string]interface{}{
				"name": name,
				"path": skillDir,
			},
		})
	}
	fmt.Printf("Skill '%s' scaffolded at %s\n", name, skillDir)
	fmt.Println("  Edit SKILL.md to add your skill instructions.")
	fmt.Println("  Then publish with: scion skills publish", skillDir)
	return nil
}

// --- skills publish ---

var skillsPublishCmd = &cobra.Command{
	Use:   "publish <path>",
	Short: "Publish a skill to the Hub",
	Long: `Publish a skill from a local directory to the Hub skill bank.

The directory must contain a SKILL.md file. If the skill already exists on the Hub,
a new version is published. Use --version to specify a version (defaults to auto-increment).

Examples:
  scion skills publish ./my-skill --scope project
  scion skills publish ./my-skill --scope global --version 1.0.0
  scion skills publish ./my-skill --scope project --name custom-name`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsPublish,
}

func runSkillsPublish(cmd *cobra.Command, args []string) error {
	skillPath := args[0]
	scope, _ := cmd.Flags().GetString("scope")
	version, _ := cmd.Flags().GetString("version")
	nameOverride, _ := cmd.Flags().GetString("name")

	// Validate the path contains a SKILL.md
	absPath, err := filepath.Abs(skillPath)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	skillMDPath := filepath.Join(absPath, "SKILL.md")
	if _, err := os.Stat(skillMDPath); os.IsNotExist(err) {
		return fmt.Errorf("no SKILL.md found in %s — is this a valid skill directory?", absPath)
	}

	// Determine skill name from directory name or --name override
	skillName := filepath.Base(absPath)
	if nameOverride != "" {
		skillName = nameOverride
	}

	if scope == "" {
		scope = "project"
	}

	// Check Hub availability
	hubCtx, err := CheckHubAvailability(projectPath)
	if err != nil {
		return err
	}
	if hubCtx == nil {
		return fmt.Errorf("Hub integration is not enabled. Use 'scion hub enable' first")
	}

	PrintUsingHub(hubCtx.Endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Get project ID for project scope
	var projectID string
	if scope == "project" {
		projectID, err = GetProjectID(hubCtx)
		if err != nil {
			return err
		}
	}

	// Collect local files
	statusf("Scanning skill files in %s...\n", absPath)
	files, err := hubclient.CollectFiles(absPath, nil)
	if err != nil {
		return fmt.Errorf("failed to scan skill files: %w", err)
	}
	statusf("Found %d file(s)\n", len(files))

	// Check if skill already exists
	var skillID string
	existingResp, listErr := hubCtx.Client.Skills().List(ctx, &hubclient.ListSkillsOptions{
		Name:      skillName,
		Scope:     scope,
		ProjectID: projectID,
		Status:    "active",
	})
	if listErr == nil {
		for _, s := range existingResp.Skills {
			if s.Name == skillName {
				skillID = s.ID
				break
			}
		}
	}

	if skillID == "" {
		// Create new skill
		statusf("Creating skill '%s' in Hub...\n", skillName)
		createResp, createErr := hubCtx.Client.Skills().Create(ctx, &hubclient.CreateSkillRequest{
			Name:      skillName,
			Scope:     scope,
			ProjectID: projectID,
		})
		if createErr != nil {
			return fmt.Errorf("failed to create skill: %w", createErr)
		}
		skillID = createResp.Skill.ID
		statusf("Skill created with ID: %s\n", skillID)
	} else {
		statusf("Skill '%s' already exists (ID: %s), publishing new version...\n", skillName, skillID)
	}

	// Publish a version
	if version == "" {
		version = "0.1.0"
	}
	statusf("Publishing version %s...\n", version)
	sv, pubErr := hubCtx.Client.Skills().PublishVersion(ctx, skillID, &hubclient.PublishVersionRequest{
		Version: version,
	})
	if pubErr != nil {
		return fmt.Errorf("failed to publish version: %w", pubErr)
	}

	// Request upload URLs
	fileReqs := make([]hubclient.FileUploadRequest, len(files))
	for i, f := range files {
		fileReqs[i] = hubclient.FileUploadRequest{
			Path: f.Path,
			Size: f.Size,
		}
	}

	statusf("Requesting upload URLs for %d file(s)...\n", len(fileReqs))
	uploadResp, uploadErr := hubCtx.Client.Skills().RequestUploadURLs(ctx, skillID, version, fileReqs)
	if uploadErr != nil {
		return fmt.Errorf("failed to get upload URLs: %w", uploadErr)
	}

	// Build local file map for lookup
	localFileMap := make(map[string]*hubclient.FileInfo)
	for i := range files {
		localFileMap[files[i].Path] = &files[i]
	}

	// Upload files
	statusf("Uploading %d file(s)...\n", len(uploadResp.UploadURLs))
	for _, urlInfo := range uploadResp.UploadURLs {
		fileInfo := localFileMap[urlInfo.Path]
		if fileInfo == nil {
			statusf("  Warning: no matching file for %s\n", urlInfo.Path)
			continue
		}

		f, openErr := os.Open(fileInfo.FullPath)
		if openErr != nil {
			return fmt.Errorf("failed to open %s: %w", fileInfo.Path, openErr)
		}

		err = hubCtx.Client.Skills().UploadFile(ctx, urlInfo.URL, urlInfo.Method, urlInfo.Headers, f)
		f.Close()
		if err != nil {
			return fmt.Errorf("failed to upload %s: %w", fileInfo.Path, err)
		}
		statusf("  Uploaded: %s\n", fileInfo.Path)
	}

	// Build manifest and finalize
	skillFiles := make([]hubclient.SkillFile, len(files))
	for i, f := range files {
		skillFiles[i] = hubclient.SkillFile{
			Path: f.Path,
			Size: f.Size,
			Hash: f.Hash,
			Mode: f.Mode,
		}
	}

	statusln("Finalizing skill version...")
	finalizedVer, finalErr := hubCtx.Client.Skills().Finalize(ctx, skillID, version, &hubclient.SkillManifest{
		Version: sv.Version,
		Files:   skillFiles,
	})
	if finalErr != nil {
		return fmt.Errorf("failed to finalize version: %w", finalErr)
	}

	if isJSONOutput() {
		return outputJSON(ActionResult{
			Status:  "success",
			Command: "skills publish",
			Message: fmt.Sprintf("Skill '%s' v%s published successfully.", skillName, version),
			Details: map[string]interface{}{
				"id":          skillID,
				"name":        skillName,
				"version":     finalizedVer.Version,
				"contentHash": finalizedVer.ContentHash,
				"scope":       scope,
				"filesCount":  len(files),
			},
		})
	}

	fmt.Printf("Skill '%s' v%s published successfully!\n", skillName, version)
	fmt.Printf("  ID:      %s\n", skillID)
	fmt.Printf("  Version: %s\n", finalizedVer.Version)
	if finalizedVer.ContentHash != "" {
		fmt.Printf("  Hash:    %s\n", truncateHash(finalizedVer.ContentHash))
	}
	return nil
}

// --- skills delete ---

var skillsDeleteCmd = &cobra.Command{
	Use:     "delete <name-or-id>",
	Aliases: []string{"rm"},
	Short:   "Delete a skill from the Hub",
	Long: `Delete a skill from the Hub skill bank.

This permanently removes the skill and all its versions.

Examples:
  scion skills delete my-skill
  scion skills delete abc123-def456`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsDelete,
}

func runSkillsDelete(cmd *cobra.Command, args []string) error {
	nameOrID := args[0]

	hubCtx, err := CheckHubAvailability(projectPath)
	if err != nil {
		return err
	}
	if hubCtx == nil {
		return fmt.Errorf("Hub integration is not enabled. Use 'scion hub enable' first")
	}

	PrintUsingHub(hubCtx.Endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Resolve skill
	skill, err := resolveSkillByNameOrID(ctx, hubCtx, nameOrID)
	if err != nil {
		return err
	}

	// Confirm deletion
	if !autoConfirm {
		fmt.Printf("Delete skill '%s' (ID: %s) from Hub? [y/N] ", skill.Name, skill.ID)
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" && confirm != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if err := hubCtx.Client.Skills().Delete(ctx, skill.ID); err != nil {
		return fmt.Errorf("failed to delete skill: %w", err)
	}

	if isJSONOutput() {
		return outputJSON(ActionResult{
			Status:  "success",
			Command: "skills delete",
			Message: fmt.Sprintf("Skill '%s' deleted successfully.", skill.Name),
			Details: map[string]interface{}{
				"id":   skill.ID,
				"name": skill.Name,
			},
		})
	}
	fmt.Printf("Skill '%s' deleted successfully.\n", skill.Name)
	return nil
}

// --- skills versions ---

var skillsVersionsCmd = &cobra.Command{
	Use:   "versions <name-or-id>",
	Short: "List versions of a skill",
	Long: `List all published versions of a skill.

Examples:
  scion skills versions security-audit
  scion skills versions abc123-def456`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsVersions,
}

func runSkillsVersions(cmd *cobra.Command, args []string) error {
	nameOrID := args[0]

	hubCtx, err := CheckHubAvailabilityWithOptions(projectPath, true)
	if err != nil {
		return err
	}
	if hubCtx == nil {
		return fmt.Errorf("Hub integration is not enabled. Use 'scion hub enable' first")
	}

	PrintUsingHub(hubCtx.Endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Resolve skill
	skill, err := resolveSkillByNameOrID(ctx, hubCtx, nameOrID)
	if err != nil {
		return err
	}

	// List versions
	resp, err := hubCtx.Client.Skills().ListVersions(ctx, skill.ID)
	if err != nil {
		return fmt.Errorf("failed to list versions: %w", err)
	}

	versions := resp.Versions

	if isJSONOutput() {
		return outputJSON(map[string]interface{}{
			"skill":    skill.Name,
			"skillId":  skill.ID,
			"versions": versions,
		})
	}

	if len(versions) == 0 {
		fmt.Printf("No versions found for skill '%s'.\n", skill.Name)
		return nil
	}

	fmt.Printf("Versions of skill '%s' (ID: %s):\n\n", skill.Name, skill.ID)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VERSION\tSTATUS\tHASH\tCREATED")
	for _, v := range versions {
		hash := truncateHash(v.ContentHash)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.Version, v.Status, hash, v.Created.Format(time.RFC3339))
	}
	w.Flush()
	return nil
}

// --- skills resolve ---

var skillsResolveCmd = &cobra.Command{
	Use:   "resolve <uri>",
	Short: "Test-resolve a skill URI",
	Long: `Resolve a skill URI to see what version and files would be used.

This is a debug/inspection command to test skill resolution without
actually provisioning an agent.

Examples:
  scion skills resolve skill://scion/core/scion@^1.0
  scion skills resolve security-audit
  scion skills resolve skill://project/my-skill@latest`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsResolve,
}

func runSkillsResolve(cmd *cobra.Command, args []string) error {
	uri := args[0]

	hubCtx, err := CheckHubAvailabilityWithOptions(projectPath, true)
	if err != nil {
		return err
	}
	if hubCtx == nil {
		return fmt.Errorf("Hub integration is not enabled. Use 'scion hub enable' first")
	}

	PrintUsingHub(hubCtx.Endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get project ID for resolution context
	var projectID string
	if pid, pidErr := GetProjectID(hubCtx); pidErr == nil {
		projectID = pid
	}

	resp, err := hubCtx.Client.Skills().Resolve(ctx, &hubclient.ResolveSkillsRequest{
		Skills: []hubclient.SkillRef{
			{URI: uri},
		},
		ProjectID: projectID,
	})
	if err != nil {
		return fmt.Errorf("failed to resolve skill: %w", err)
	}

	if isJSONOutput() {
		return outputJSON(resp)
	}

	// Display errors
	for _, e := range resp.Errors {
		fmt.Fprintf(os.Stderr, "Error resolving %s: %s\n", e.URI, e.Error)
	}

	// Display warnings
	for _, w := range resp.Warnings {
		fmt.Fprintf(os.Stderr, "Warning for %s: %s\n", w.URI, w.Message)
	}

	// Display resolved skills
	for _, r := range resp.Resolved {
		fmt.Printf("URI:      %s\n", r.URI)
		fmt.Printf("Name:     %s\n", r.Name)
		fmt.Printf("Version:  %s\n", r.ResolvedVersion)
		if r.ContentHash != "" {
			fmt.Printf("Hash:     %s\n", truncateHash(r.ContentHash))
		}
		if len(r.Files) > 0 {
			fmt.Printf("Files:    %d\n", len(r.Files))
			for _, f := range r.Files {
				fmt.Printf("  - %s\n", f.Path)
			}
		}
	}

	if len(resp.Resolved) == 0 && len(resp.Errors) == 0 {
		fmt.Println("No results returned.")
	}

	return nil
}

// --- helpers ---

// resolveSkillByNameOrID finds a skill by name (searching via list) or by direct ID lookup.
func resolveSkillByNameOrID(ctx context.Context, hubCtx *HubContext, nameOrID string) (*hubclient.Skill, error) {
	// First, try by name (list with name filter)
	resp, err := hubCtx.Client.Skills().List(ctx, &hubclient.ListSkillsOptions{
		Name: nameOrID,
	})
	if err == nil && len(resp.Skills) > 0 {
		return &resp.Skills[0], nil
	}

	// If name lookup failed or returned nothing, try by ID
	skill, err := hubCtx.Client.Skills().Get(ctx, nameOrID)
	if err != nil {
		return nil, fmt.Errorf("skill '%s' not found", nameOrID)
	}
	return skill, nil
}

// --- init ---

func init() {
	rootCmd.AddCommand(skillsCmd)

	// Subcommands
	skillsCmd.AddCommand(skillsListCmd)
	skillsCmd.AddCommand(skillsShowCmd)
	skillsCmd.AddCommand(skillsCreateCmd)
	skillsCmd.AddCommand(skillsPublishCmd)
	skillsCmd.AddCommand(skillsDeleteCmd)
	skillsCmd.AddCommand(skillsVersionsCmd)
	skillsCmd.AddCommand(skillsResolveCmd)

	// Flags: list
	skillsListCmd.Flags().String("scope", "", "Filter by scope (global, project, user)")
	skillsListCmd.Flags().String("search", "", "Search by name or description")

	// Flags: create
	skillsCreateCmd.Flags().StringP("output", "o", "", "Output directory (default: current directory)")

	// Flags: publish
	skillsPublishCmd.Flags().String("scope", "project", "Scope for the skill (global, project, user)")
	skillsPublishCmd.Flags().String("version", "", "Version to publish (default: auto)")
	skillsPublishCmd.Flags().String("name", "", "Override skill name (default: directory name)")

	// Also add a 'skill' alias (singular) for convenience, matching the template/templates pattern.
	skillCmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage skill bank skills (alias for 'skills')",
		Long:  skillsCmd.Long,
	}
	rootCmd.AddCommand(skillCmd)

	skillCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List available skills",
		RunE:  runSkillsList,
	})

	showAlias := &cobra.Command{
		Use:   "show <name-or-id>",
		Short: "Show skill details",
		Args:  cobra.ExactArgs(1),
		RunE:  runSkillsShow,
	}
	skillCmd.AddCommand(showAlias)

	createAlias := &cobra.Command{
		Use:   "create <name>",
		Short: "Scaffold a new skill directory",
		Args:  cobra.ExactArgs(1),
		RunE:  runSkillsCreate,
	}
	createAlias.Flags().StringP("output", "o", "", "Output directory (default: current directory)")
	skillCmd.AddCommand(createAlias)

	publishAlias := &cobra.Command{
		Use:   "publish <path>",
		Short: "Publish a skill to the Hub",
		Args:  cobra.ExactArgs(1),
		RunE:  runSkillsPublish,
	}
	publishAlias.Flags().String("scope", "project", "Scope for the skill (global, project, user)")
	publishAlias.Flags().String("version", "", "Version to publish (default: auto)")
	publishAlias.Flags().String("name", "", "Override skill name (default: directory name)")
	skillCmd.AddCommand(publishAlias)

	deleteAlias := &cobra.Command{
		Use:     "delete <name-or-id>",
		Aliases: []string{"rm"},
		Short:   "Delete a skill from the Hub",
		Args:    cobra.ExactArgs(1),
		RunE:    runSkillsDelete,
	}
	skillCmd.AddCommand(deleteAlias)

	versionsAlias := &cobra.Command{
		Use:   "versions <name-or-id>",
		Short: "List versions of a skill",
		Args:  cobra.ExactArgs(1),
		RunE:  runSkillsVersions,
	}
	skillCmd.AddCommand(versionsAlias)

	resolveAlias := &cobra.Command{
		Use:   "resolve <uri>",
		Short: "Test-resolve a skill URI",
		Args:  cobra.ExactArgs(1),
		RunE:  runSkillsResolve,
	}
	skillCmd.AddCommand(resolveAlias)
}
