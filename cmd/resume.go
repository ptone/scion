package cmd

import (
	"github.com/spf13/cobra"
)

// resumeCmd represents the resume command
var resumeCmd = &cobra.Command{
	Use:   "resume <agent-name>",
	Short: "Resume a stopped scion agent",
	Long: `Resume an existing stopped LLM agent. 
The agent will be re-launched with the harness-specific resume flag, 
preserving its previous state.

The agent-name is required as the first argument. All subsequent arguments
form the task prompt to be added to the resumed session (if supported by the harness).`,
	Args: cobra.MinimumNArgs(1),
	ValidArgsFunction: getAgentNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunAgent(cmd, args, true)
	},
}

func init() {
	rootCmd.AddCommand(resumeCmd)
	resumeCmd.Flags().StringVarP(&templateName, "type", "t", "", "Template to use")
	resumeCmd.Flags().StringVarP(&agentImage, "image", "i", "", "Container image to use (overrides template)")
	resumeCmd.Flags().BoolVar(&noAuth, "no-auth", false, "Disable authentication propagation")
	resumeCmd.Flags().BoolVarP(&attach, "attach", "a", false, "Attach to the agent TTY after starting")
}