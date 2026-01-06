package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/ptone/scion-agent/pkg/agent"
	"github.com/ptone/scion-agent/pkg/runtime"
	"github.com/spf13/cobra"
)

var deleteStopped bool

// deleteCmd represents the delete command
var deleteCmd = &cobra.Command{
	Use:     "delete <agent>",
	Aliases: []string{"rm"},
	Short:   "Delete an agent",
	Long:    `Stop and remove an agent container and its associated files and worktree.`,
	ValidArgsFunction: getAgentNames,
	Args: func(cmd *cobra.Command, args []string) error {
		if deleteStopped {
			if len(args) > 0 {
				return fmt.Errorf("no arguments allowed when using --stopped")
			}
			return nil
		}
		if len(args) != 1 {
			return fmt.Errorf("requires exactly 1 argument (agent name)")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if deleteStopped {
			rt := runtime.GetRuntime(grovePath, profile)
			mgr := agent.NewManager(rt)
			agents, err := mgr.List(context.Background(), nil)
			if err != nil {
				return err
			}

			var deletedCount int
			for _, a := range agents {
				if a.ID == "" {
					continue // No container
				}

				status := strings.ToLower(a.ContainerStatus)
				// Check if running
				if strings.HasPrefix(status, "up") ||
					strings.HasPrefix(status, "running") ||
					strings.HasPrefix(status, "pending") ||
					strings.HasPrefix(status, "restarting") {
					continue
				}

				fmt.Printf("Deleting stopped agent '%s' (status: %s)...\n", a.Name, a.ContainerStatus)

				targetGrovePath := a.GrovePath
				if targetGrovePath == "" {
					targetGrovePath = grovePath
				}

				branchDeleted, err := mgr.Delete(context.Background(), a.Name, true, targetGrovePath, removeBranch)
				if err != nil {
					fmt.Printf("Failed to delete agent '%s': %v\n", a.Name, err)
					continue
				}

				if branchDeleted {
					fmt.Printf("Git branch associated with agent '%s' deleted.\n", a.Name)
				}
				fmt.Printf("Agent '%s' deleted.\n", a.Name)
				deletedCount++
			}

			if deletedCount == 0 {
				fmt.Println("No stopped agents found.")
			}
			return nil
		}

		agentName := args[0]

		effectiveProfile := profile
		if effectiveProfile == "" {
			effectiveProfile = agent.GetSavedRuntime(agentName, grovePath)
		}

		rt := runtime.GetRuntime(grovePath, effectiveProfile)
		mgr := agent.NewManager(rt)

		fmt.Printf("Deleting agent '%s'...\n", agentName)

		// Try to stop first, ignore error if already stopped
		_ = mgr.Stop(context.Background(), agentName)

		// We check if it exists in List to provide better feedback
		agents, _ := mgr.List(context.Background(), map[string]string{"scion.name": agentName})
		containerFound := false
		for _, a := range agents {
			if a.Name == agentName || a.ID == agentName || strings.TrimPrefix(a.Name, "/") == agentName {
				containerFound = true
				break
			}
		}

		if !containerFound {
			fmt.Println("No container found, removing agent definition...")
		}

		branchDeleted, err := mgr.Delete(context.Background(), agentName, true, grovePath, removeBranch)
		if err != nil {
			return err
		}

		if branchDeleted {
			fmt.Printf("Git branch associated with agent '%s' deleted.\n", agentName)
		}

		fmt.Printf("Agent '%s' deleted.\n", agentName)
		return nil
	},
}

var removeBranch bool

func init() {
	rootCmd.AddCommand(deleteCmd)
	deleteCmd.Flags().BoolVarP(&removeBranch, "remove-branch", "b", false, "Remove the git branch associated with the worktree")
	deleteCmd.Flags().BoolVar(&deleteStopped, "stopped", false, "Delete all agents with stopped containers")
}