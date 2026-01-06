package cmd

import (
	"github.com/spf13/cobra"
)

// startCmd represents the start command
var startCmd = &cobra.Command{
	Use:     "start <agent-name> [task...]",
	Aliases: []string{"run"},
	Short:   "Launch a new scion agent",
	Long: `Provision and launch a new isolated LLM agent to perform a specific task.
The agent will be created from a template and run in a detached container.

The agent-name is required as the first argument. All subsequent arguments 
form the task prompt. If no task arguments are provided, the agent will 
look for a prompt.md file in its root directory.`,
	Args: cobra.MinimumNArgs(1),
	ValidArgsFunction: getAgentNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunAgent(cmd, args, false)
	},
}

func init() {

	rootCmd.AddCommand(startCmd)

	startCmd.Flags().StringVarP(&templateName, "type", "t", "", "Template to use")

	startCmd.Flags().StringVarP(&agentImage, "image", "i", "", "Container image to use (overrides template)")

	startCmd.Flags().BoolVar(&noAuth, "no-auth", false, "Disable authentication propagation")

	startCmd.Flags().BoolVarP(&attach, "attach", "a", false, "Attach to the agent TTY after starting")

	startCmd.Flags().StringVarP(&branch, "branch", "b", "", "Git branch to use for the agent workspace")

}
