package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ptone/scion-agent/pkg/agent"
	"github.com/ptone/scion-agent/pkg/runtime"
	"github.com/spf13/cobra"
)

// logsCmd represents the logs command
var logsCmd = &cobra.Command{
	Use:   "logs <agent>",
	Short: "Get logs of an agent",
	Args:  cobra.ExactArgs(1),
	ValidArgsFunction: getAgentNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		agentName := args[0]

		effectiveProfile := profile
		if effectiveProfile == "" {
			effectiveProfile = agent.GetSavedRuntime(agentName, grovePath)
		}

		rt := runtime.GetRuntime(grovePath, effectiveProfile)

		// 1. Try to find the agent to get its grove path
		agents, err := rt.List(context.Background(), map[string]string{
			"scion.agent": "true",
			"scion.name":  agentName,
		})

		if err == nil && len(agents) > 0 {
			a := agents[0]
			if a.GrovePath != "" {
				agentLogPath := filepath.Join(a.GrovePath, "agents", agentName, "home", "agent.log")
				if data, err := os.ReadFile(agentLogPath); err == nil {
					fmt.Print(string(data))
					return nil
				}
			}
		}

		// 2. Fallback to container logs if file not found or List failed
		logs, err := rt.GetLogs(context.Background(), agentName)
		if err != nil {
			return err
		}

		fmt.Println(logs)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
}
