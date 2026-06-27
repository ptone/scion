package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

func TestCloudRunNFSExportPaths(t *testing.T) {
	paths, err := cloudRunNFSExportPaths("/scion-workspaces/", "proj-123", "agent-456")
	if err != nil {
		t.Fatalf("cloudRunNFSExportPaths: %v", err)
	}

	if paths.workspaceExportPath != "/scion-workspaces/projects/proj-123/workspace" {
		t.Errorf("workspaceExportPath = %q", paths.workspaceExportPath)
	}
	if paths.homeExportPath != "/scion-workspaces/projects/proj-123/agents/agent-456/home" {
		t.Errorf("homeExportPath = %q", paths.homeExportPath)
	}
	if paths.secretsExportPath != "/scion-workspaces/projects/proj-123/agents/agent-456/secrets" {
		t.Errorf("secretsExportPath = %q", paths.secretsExportPath)
	}
}

func TestCloudRunNFSExportPathsRejectUnsafeInputs(t *testing.T) {
	tests := []struct {
		name      string
		export    string
		projectID string
		agentID   string
		want      string
	}{
		{
			name:      "relative export",
			export:    "scion-workspaces",
			projectID: "proj-123",
			agentID:   "agent-456",
			want:      "nfs_export must be an absolute server path",
		},
		{
			name:      "project traversal",
			export:    "/scion-workspaces",
			projectID: "../proj-123",
			agentID:   "agent-456",
			want:      "project_id",
		},
		{
			name:      "agent slash",
			export:    "/scion-workspaces",
			projectID: "proj-123",
			agentID:   "agents/agent-456",
			want:      "agent_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cloudRunNFSExportPaths(tt.export, tt.projectID, tt.agentID)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCloudRunNFSHostPaths(t *testing.T) {
	hostWorkspace := filepath.Join(string(filepath.Separator), "mnt", "nfs", "share1", "projects", "proj-123", "workspace")

	paths, err := cloudRunNFSHostPaths(hostWorkspace, "proj-123", "agent-456")
	if err != nil {
		t.Fatalf("cloudRunNFSHostPaths: %v", err)
	}

	wantHostBase := filepath.Join(string(filepath.Separator), "mnt", "nfs", "share1")
	if paths.hostBase != wantHostBase {
		t.Errorf("hostBase = %q, want %q", paths.hostBase, wantHostBase)
	}
	if paths.homeHostPath != filepath.Join(wantHostBase, "projects", "proj-123", "agents", "agent-456", "home") {
		t.Errorf("homeHostPath = %q", paths.homeHostPath)
	}
	if paths.secretsHostPath != filepath.Join(wantHostBase, "projects", "proj-123", "agents", "agent-456", "secrets") {
		t.Errorf("secretsHostPath = %q", paths.secretsHostPath)
	}
}

func TestCloudRunNFSHostPathsRejectLocalAssumptions(t *testing.T) {
	tests := []struct {
		name          string
		hostWorkspace string
		want          string
	}{
		{
			name:          "relative",
			hostWorkspace: filepath.Join("mnt", "nfs", "share1", "projects", "proj-123", "workspace"),
			want:          "must be absolute",
		},
		{
			name:          "wrong suffix",
			hostWorkspace: filepath.Join(string(filepath.Separator), "tmp", "proj-123"),
			want:          "must end with",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cloudRunNFSHostPaths(tt.hostWorkspace, "proj-123", "agent-456")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCloudRunProvisionNFSRequiresWorkspaceHostPath(t *testing.T) {
	r := &CloudRunRuntime{config: &config.CloudRunInstancesConfig{
		ProjectID: "gcp-project",
		Location:  "us-central1",
		NFSServer: "10.0.0.2",
		NFSExport: "/scion-workspaces",
	}}

	_, err := r.provisionCloudRunNFS(context.Background(), RunConfig{
		WorkspaceBackendName: "nfs",
		ProjectID:            "proj-123",
	}, "agent-456", 1000, 1000)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "RunConfig.Workspace is empty") {
		t.Fatalf("error = %q", err)
	}
}

func TestCloudRunProvisionNFSFailsWhenHubLacksNFSMount(t *testing.T) {
	root := t.TempDir()
	missingMount := filepath.Join(root, "missing-share")
	workspace := filepath.Join(missingMount, "projects", "proj-123", "workspace")
	r := &CloudRunRuntime{config: &config.CloudRunInstancesConfig{
		ProjectID: "gcp-project",
		Location:  "us-central1",
		NFSServer: "10.0.0.2",
		NFSExport: "/scion-workspaces",
	}}

	_, err := r.provisionCloudRunNFS(context.Background(), RunConfig{
		WorkspaceBackendName: "nfs",
		ProjectID:            "proj-123",
		Workspace:            workspace,
	}, "agent-456", 1000, 1000)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Hub cannot access NFS export") {
		t.Fatalf("error = %q", err)
	}
}
