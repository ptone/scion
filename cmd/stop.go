package cmd

import (
	"context"
	"fmt"

	"github.com/ptone/scion-agent/pkg/agent"
	"github.com/ptone/scion-agent/pkg/runtime"
	"github.com/spf13/cobra"
)

var stopRm bool

// stopCmd represents the stop command
var stopCmd = &cobra.Command{
	Use:   "stop <agent>",
	Short: "Stop an agent",
	Args:  cobra.ExactArgs(1),
	ValidArgsFunction: getAgentNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		agentName := args[0]

		effectiveProfile := profile
		if effectiveProfile == "" {
			effectiveProfile = agent.GetSavedProfile(agentName, grovePath)
		}

		rt := runtime.GetRuntime(grovePath, effectiveProfile)
		mgr := agent.NewManager(rt)

		fmt.Printf("Stopping agent '%s'...\n", agentName)
		if err := mgr.Stop(context.Background(), agentName); err != nil {
			return err
		}

		_ = agent.UpdateAgentConfig(agentName, grovePath, "stopped", "", "", "")

		if stopRm {
			if _, err := mgr.Delete(context.Background(), agentName, true, grovePath, false); err != nil {
				return err
			}
			fmt.Printf("Agent '%s' stopped and removed.\n", agentName)
		} else {
			fmt.Printf("Agent '%s' stopped.\n", agentName)
		}
		
		return nil
	},
}

func init() {
	stopCmd.Flags().BoolVar(&stopRm, "rm", false, "Remove the agent after stopping")
	rootCmd.AddCommand(stopCmd)
}