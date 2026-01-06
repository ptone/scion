package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestGetAgentNames(t *testing.T) {
	// Setup temp directory for grove
	tmpDir, err := os.MkdirTemp("", "scion-completion-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	groveDir := filepath.Join(tmpDir, ".scion")
	agentsDir := filepath.Join(groveDir, "agents")
	err = os.MkdirAll(agentsDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Create some dummy agents
	createAgent := func(name string) {
		agentDir := filepath.Join(agentsDir, name)
		err := os.MkdirAll(agentDir, 0755)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(agentDir, "scion-agent.json"))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	createAgent("agent1")
	createAgent("agent2")
	createAgent("foobar")

	// Create a directory that is NOT an agent
	err = os.MkdirAll(filepath.Join(agentsDir, "not-an-agent"), 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Mock command
	cmd := &cobra.Command{}
	cmd.Flags().String("grove", "", "")
	cmd.Flags().Bool("global", false, "")

	// Test with explicit grove path
	cmd.Flags().Set("grove", groveDir)

	t.Run("Complete all agents", func(t *testing.T) {
		names, _ := getAgentNames(cmd, []string{}, "")
		assert.Contains(t, names, "agent1")
		assert.Contains(t, names, "agent2")
		assert.Contains(t, names, "foobar")
		assert.NotContains(t, names, "not-an-agent")
		assert.Len(t, names, 3)
	})

	t.Run("Complete prefix", func(t *testing.T) {
		names, _ := getAgentNames(cmd, []string{}, "agent")
		assert.Contains(t, names, "agent1")
		assert.Contains(t, names, "agent2")
		assert.NotContains(t, names, "foobar")
		assert.Len(t, names, 2)
	})
	
	t.Run("Complete no match", func(t *testing.T) {
		names, _ := getAgentNames(cmd, []string{}, "z")
		assert.Len(t, names, 0)
	})

	t.Run("Args present", func(t *testing.T) {
		names, _ := getAgentNames(cmd, []string{"already-have-arg"}, "")
		assert.Nil(t, names)
	})
}
