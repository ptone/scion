/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
)

var (
	secretType         string
	secretTarget       string
	secretForce        bool
	secretScope        string
	secretAllowProgeny bool
)

var secretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Manage agent secrets",
	Long: `Commands for managing secrets from within an agent container.

To read a secret value, use 'scion secret get --value'; to list secrets, use
'scion secret list'.`,
}

var secretSetCmd = &cobra.Command{
	Use:   "set KEY VALUE",
	Short: "Store a secret via the Hub API",
	Long: `Store a secret in the Hub from within an agent container.

By default, the secret is scoped to the current agent's project. Subsequent
agents in the same project will receive this secret automatically. Use
--scope=user to store a personal credential visible only to your agents.

Environment secret names that the shell, a runtime or a build tool acts on
are refused: exact names (PATH, HOME, BASH_ENV, NODE_OPTIONS, GOPROXY, ...),
prefixes (LD_, GIT_, GITHUB_TOKEN, CLOUDSDK_, PIP_, ...) and suffixes (_PATH,
_HOME, _OPTS, _OPTIONS, _COMMAND, _PROXY, _CERT_FILE, ...). The rules ignore
case.

If VALUE starts with @, the remainder is treated as a file path. The file
contents are read and base64-encoded, and --type defaults to "file". A file
secret's target must be ~/.scion/agent-secrets/<name> or a harness
credential file (for example ~/.claude/.credentials.json). Without
--target, a harness credential file under your home keeps its ~/ path, and
any other file defaults to ~/.scion/agent-secrets/<KEY>; KEY must then start
with a letter or digit.

Examples:
  # Store a simple environment variable secret
  sciontool secret set MY_API_KEY "sk-abc123"

  # Store a credential file
  sciontool secret set CLAUDE_AUTH @~/.claude/.credentials.json

  # Store a file secret with explicit target path
  sciontool secret set MY_CERT @/tmp/cert.pem --type file --target ~/.scion/agent-secrets/cert.pem

  # Overwrite an existing secret
  sciontool secret set MY_KEY "new-value" --force

  # Store a user-scoped (personal) secret
  sciontool secret set MY_TOKEN "tok-123" --scope user`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		value := args[1]

		if key == "" {
			log.Error("key cannot be empty")
			os.Exit(1)
		}
		if strings.ContainsAny(key, "= \t\n") {
			log.Error("key cannot contain spaces, tabs, newlines, or '='")
			os.Exit(1)
		}

		// Validate scope flag.
		if secretScope != "" && secretScope != "project" && secretScope != "user" {
			log.Error("--scope must be \"project\" or \"user\"")
			os.Exit(1)
		}

		localType := secretType
		localTarget := secretTarget

		// Handle @file syntax: read file and base64-encode contents.
		if strings.HasPrefix(value, "@") {
			filePath := value[1:]
			if filePath == "~" || strings.HasPrefix(filePath, "~/") {
				home, err := os.UserHomeDir()
				if err != nil {
					log.Error("Failed to expand home directory: %v", err)
					os.Exit(1)
				}
				filePath = filepath.Join(home, strings.TrimPrefix(filePath[1:], "/"))
			}
			info, err := os.Stat(filePath)
			if err != nil {
				log.Error("Failed to stat file %s: %v", filePath, err)
				os.Exit(1)
			}
			if info.Size() > 64*1024 {
				log.Error("File exceeds 64KB limit (%d bytes)", info.Size())
				os.Exit(1)
			}
			data, err := os.ReadFile(filePath)
			if err != nil {
				log.Error("Failed to read file %s: %v", filePath, err)
				os.Exit(1)
			}
			value = base64.StdEncoding.EncodeToString(data)
			if localType == "" {
				localType = "file"
			}
			if localTarget == "" {
				absPath, err := filepath.Abs(filePath)
				if err != nil {
					log.Error("Failed to resolve absolute path for %s: %v", filePath, err)
					os.Exit(1)
				}
				home, _ := os.UserHomeDir()
				localTarget, err = defaultFileTarget(absPath, home, key)
				if err != nil {
					log.Error("%v", err)
					os.Exit(1)
				}
			}
		} else {
			value = base64.StdEncoding.EncodeToString([]byte(value))
		}

		hubClient := hub.NewClient()
		if hubClient == nil || !hubClient.IsConfigured() {
			log.Error("Hub client not configured. Is SCION_HUB_ENDPOINT set?")
			os.Exit(1)
		}

		// Resolve allowProgeny. User-scoped captured secrets should always
		// enable progeny so spawned children inherit the credential. If the
		// caller didn't set --allow-progeny explicitly and the scope is user,
		// default to true; otherwise honour the flag value.
		var allowProgeny *bool
		if cmd.Flags().Changed("allow-progeny") {
			allowProgeny = &secretAllowProgeny
		} else if secretScope == "user" {
			t := true
			allowProgeny = &t
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		resp, err := hubClient.SetSecret(ctx, key, value, localType, localTarget, secretScope, secretForce, allowProgeny)
		if err != nil {
			log.Error("%v", err)
			os.Exit(1)
		}

		log.Info("Secret %q stored (scope: %s)", resp.Key, resp.Scope)
	},
}

// agentSecretsDir, agentSecretLeaf and knownCredentialFileTargets mirror the
// hub's agent file-target policy (pkg/secret/agent_policy.go); sciontool does
// not import pkg/secret. secret_target_test.go keeps them in step.
const agentSecretsDir = "~/.scion/agent-secrets/"

var agentSecretLeaf = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var knownCredentialFileTargets = []string{
	"~/.claude/.credentials.json",
	"~/.codex/auth.json",
	"~/.config/gcloud/application_default_credentials.json",
	"~/.gemini/antigravity-cli/antigravity-oauth-token",
	"~/.gemini/oauth_creds.json",
	"~/.grok/auth.json",
	"~/.local/share/opencode/auth.json",
	"~/.pi/agent/auth.json",
}

// defaultFileTarget is the file secret target used when --target is not
// given: a harness credential file under home keeps its ~/ path, and any
// other file goes to agentSecretsDir + key, the only other target the hub
// accepts for a secret written from an agent. A key that is not a valid
// file name there is refused.
func defaultFileTarget(absPath, home, key string) (string, error) {
	if home != "" {
		if rel, ok := strings.CutPrefix(absPath, home+"/"); ok {
			if t := "~/" + rel; slices.Contains(knownCredentialFileTargets, t) {
				return t, nil
			}
		}
	}
	if !agentSecretLeaf.MatchString(key) {
		return "", fmt.Errorf("cannot default the target for key %q: it must start with a letter or digit; pass --target %s<name>", key, agentSecretsDir)
	}
	return agentSecretsDir + key, nil
}

func init() {
	rootCmd.AddCommand(secretCmd)
	secretCmd.AddCommand(secretSetCmd)
	// get and list moved to the scion CLI (ptone/scion#5013); set stays
	// for the harness capture and provisioning scripts.
	secretCmd.AddCommand(movedSubcommand("get", "secret get", "secret get --value"))
	secretCmd.AddCommand(movedSubcommand("list", "secret list", "secret list"))

	secretSetCmd.Flags().StringVar(&secretType, "type", "", "Secret type: environment (default), variable, file")
	secretSetCmd.Flags().StringVar(&secretTarget, "target", "", "Injection target path (defaults to key for env, required for file)")
	secretSetCmd.Flags().BoolVar(&secretForce, "force", false, "Overwrite existing secret")
	secretSetCmd.Flags().StringVar(&secretScope, "scope", "", "Secret scope: project (default) or user")
	secretSetCmd.Flags().BoolVar(&secretAllowProgeny, "allow-progeny", false, "Allow progeny agents to inherit this secret (user scope only)")
}
