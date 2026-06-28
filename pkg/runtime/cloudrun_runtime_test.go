package runtime

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

func TestNewCloudRunRuntimeValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.CloudRunInstancesConfig
		want string
	}{
		{
			name: "nil config",
			cfg:  nil,
			want: "cannot be nil",
		},
		{
			name: "missing project id",
			cfg: &config.CloudRunInstancesConfig{
				Location: "us-central1",
			},
			want: "ProjectID must be non-empty",
		},
		{
			name: "missing location",
			cfg: &config.CloudRunInstancesConfig{
				ProjectID: "gcp-project",
			},
			want: "Location must be a valid GCP region",
		},
		{
			name: "invalid location",
			cfg: &config.CloudRunInstancesConfig{
				ProjectID: "gcp-project",
				Location:  "moon",
			},
			want: "Location must be a valid GCP region",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCloudRunRuntime(tt.cfg)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestNewCloudRunRuntimeValidConfig(t *testing.T) {
	rt, err := NewCloudRunRuntime(&config.CloudRunInstancesConfig{
		ProjectID: "gcp-project",
		Location:  "us-central1",
	})
	if err != nil {
		t.Fatalf("NewCloudRunRuntime: %v", err)
	}
	if rt.Name() != "cloudrun" {
		t.Fatalf("Name() = %q, want cloudrun", rt.Name())
	}
}

func TestCloudRunInstanceIDStableFromAgentID(t *testing.T) {
	got := cloudRunInstanceID("Agent 123")
	if got != cloudRunInstanceID("Agent 123") {
		t.Fatal("cloudRunInstanceID is not stable for the same agent ID")
	}
	if got == cloudRunInstanceID("agent-123") {
		t.Fatal("distinct raw agent IDs should not collapse to the same instance ID")
	}
	if len(got) > cloudRunInstanceIDMaxLength {
		t.Fatalf("instance ID length = %d, want <= %d", len(got), cloudRunInstanceIDMaxLength)
	}
	if !regexp.MustCompile(`^agent-[a-z0-9][a-z0-9-]*[a-z0-9]$`).MatchString(got) {
		t.Fatalf("instance ID %q is not a valid lowercase hyphenated name", got)
	}
}

func TestCloudRunInstanceIDHandlesLongAndUnsafeAgentID(t *testing.T) {
	got := cloudRunInstanceID(strings.Repeat("Agent_With_Unsafe_Characters_", 8))
	if len(got) > cloudRunInstanceIDMaxLength {
		t.Fatalf("instance ID length = %d, want <= %d", len(got), cloudRunInstanceIDMaxLength)
	}
	if !strings.HasPrefix(got, "agent-agent-with-unsafe-characters") {
		t.Fatalf("instance ID = %q, want readable slug prefix", got)
	}
}

func TestCloudRunImageOperationsAreRemoteNoops(t *testing.T) {
	rt := &CloudRunRuntime{}

	exists, err := rt.ImageExists(context.Background(), "us-docker.pkg.dev/project/repo/image:tag")
	if err != nil {
		t.Fatalf("ImageExists returned error: %v", err)
	}
	if !exists {
		t.Fatal("ImageExists = false, want true because Cloud Run resolves remote images")
	}
	if err := rt.PullImage(context.Background(), "us-docker.pkg.dev/project/repo/image:tag"); err != nil {
		t.Fatalf("PullImage returned error: %v", err)
	}
}

func TestBuildCloudRunInstanceWiresRuntimeConfigAndNFSMounts(t *testing.T) {
	rt := &CloudRunRuntime{config: &config.CloudRunInstancesConfig{
		ServiceAccount: "agent-runtime@gcp-project.iam.gserviceaccount.com",
		Network:        "projects/gcp-project/global/networks/scion",
		Subnetwork:     "projects/gcp-project/regions/us-central1/subnetworks/agents",
		NFSServer:      "10.0.0.2",
		NFSExport:      "/scion-workspaces",
	}}

	inst := rt.buildCloudRunInstance(RunConfig{
		Image:              "us-docker.pkg.dev/gcp-project/scion/agent:latest",
		ContainerWorkspace: "/workspace",
		Labels: map[string]string{
			"agent_id": "agent-456",
		},
	}, 1000, 1000, &cloudRunNFSProvisionPaths{
		workspaceExportPath: "/scion-workspaces/projects/proj-123/workspace",
		homeExportPath:      "/scion-workspaces/projects/proj-123/agents/agent-456/home",
		secretsExportPath:   "/scion-workspaces/projects/proj-123/agents/agent-456/secrets",
	})

	if inst.ServiceAccount != "agent-runtime@gcp-project.iam.gserviceaccount.com" {
		t.Fatalf("ServiceAccount = %q", inst.ServiceAccount)
	}
	if inst.VpcAccess == nil || len(inst.VpcAccess.NetworkInterfaces) != 1 {
		t.Fatalf("VpcAccess.NetworkInterfaces = %+v", inst.VpcAccess)
	}
	if got := inst.VpcAccess.NetworkInterfaces[0].Network; got != "projects/gcp-project/global/networks/scion" {
		t.Fatalf("VpcAccess network = %q", got)
	}
	if got := inst.VpcAccess.NetworkInterfaces[0].Subnetwork; got != "projects/gcp-project/regions/us-central1/subnetworks/agents" {
		t.Fatalf("VpcAccess subnetwork = %q", got)
	}

	if len(inst.Volumes) != 3 {
		t.Fatalf("Volumes length = %d, want 3", len(inst.Volumes))
	}
	wantVolumes := map[string]struct {
		path     string
		readOnly bool
	}{
		"workspace": {path: "/scion-workspaces/projects/proj-123/workspace"},
		"home":      {path: "/scion-workspaces/projects/proj-123/agents/agent-456/home"},
		"secrets":   {path: "/scion-workspaces/projects/proj-123/agents/agent-456/secrets", readOnly: true},
	}
	for _, volume := range inst.Volumes {
		want, ok := wantVolumes[volume.Name]
		if !ok {
			t.Fatalf("unexpected volume %q", volume.Name)
		}
		nfs := volume.GetNfs()
		if nfs == nil {
			t.Fatalf("volume %q has nil NFS source", volume.Name)
		}
		if nfs.Server != "10.0.0.2" || nfs.Path != want.path || nfs.ReadOnly != want.readOnly {
			t.Fatalf("volume %q NFS = %+v", volume.Name, nfs)
		}
	}

	mounts := inst.Containers[0].VolumeMounts
	wantMounts := map[string]string{
		"workspace": "/workspace",
		"home":      "/home/scion",
		"secrets":   "/home/scion/.scion/secrets",
	}
	if len(mounts) != len(wantMounts) {
		t.Fatalf("VolumeMounts length = %d, want %d", len(mounts), len(wantMounts))
	}
	for _, mount := range mounts {
		if got, want := mount.MountPath, wantMounts[mount.Name]; got != want {
			t.Fatalf("mount %q path = %q, want %q", mount.Name, got, want)
		}
	}
}

func TestCloudRunDeferredRuntimeMethodsReturnExplicitErrors(t *testing.T) {
	rt := &CloudRunRuntime{}

	if err := rt.Sync(context.Background(), "agent-1", SyncTo); err == nil || !strings.Contains(err.Error(), "Hub workspace API") {
		t.Fatalf("Sync error = %v, want Hub workspace API guidance", err)
	}
	if _, err := rt.GetWorkspacePath(context.Background(), "agent-1"); err == nil || !strings.Contains(err.Error(), "host workspace paths are not available") {
		t.Fatalf("GetWorkspacePath error = %v, want explicit unsupported error", err)
	}
}

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
