package cmd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ptone/scion-agent/pkg/config"
	"github.com/spf13/cobra"
)

func getAgentNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var names []string
	seen := make(map[string]bool)

	// Helper to scan a grove directory
	scanGrove := func(groveDir string) {
		if groveDir == "" {
			return
		}
		agentsDir := filepath.Join(groveDir, "agents")
		entries, err := os.ReadDir(agentsDir)
		if err != nil {
			return
		}

		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), toComplete) {
				// Verify it looks like an agent (has scion-agent.json)
				// This check might be too slow for thousands of dirs, but for typical usage it's fine
				// and prevents completing random directories.
				if _, err := os.Stat(filepath.Join(agentsDir, e.Name(), "scion-agent.json")); err == nil {
					if !seen[e.Name()] {
						names = append(names, e.Name())
						seen[e.Name()] = true
					}
				}
			}
		}
	}

	// 1. Scan local/current grove
	// We need to resolve the grove path. cmd.Flag("grove") might not be parsed yet during completion,
	// so we check the flag explicitly or default logic.
	// Cobra completion happens before full flag parsing in some cases, or we can inspect flags.
	
	// Try to get grove from flag if specified by user in the command line so far
	currentGrovePath, _ := cmd.Flags().GetString("grove")
	
	// If global flag is set
	global, _ := cmd.Flags().GetBool("global")
	if global {
		currentGrovePath = "global"
	}

	resolvedPath, _ := config.GetResolvedProjectDir(currentGrovePath)
	scanGrove(resolvedPath)

	// 2. Scan global grove if not already scanned
	globalDir, _ := config.GetGlobalDir()
	if globalDir != "" && globalDir != resolvedPath {
		scanGrove(globalDir)
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}
