package fswatcher

import (
	"context"
	"fmt"
	"log"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// GroveDiscovery discovers watch directories from running Docker containers
// that belong to a specific grove.
type GroveDiscovery struct {
	dockerClient *client.Client
	groveID      string
	verbose      bool
}

// NewGroveDiscovery creates a GroveDiscovery for the given grove ID.
func NewGroveDiscovery(dockerClient *client.Client, groveID string, verbose bool) *GroveDiscovery {
	return &GroveDiscovery{
		dockerClient: dockerClient,
		groveID:      groveID,
		verbose:      verbose,
	}
}

// Discover returns the set of host directories to watch, discovered from
// container bind mounts for all containers in the grove.
func (g *GroveDiscovery) Discover(ctx context.Context) ([]string, error) {
	containers, err := g.dockerClient.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("scion.grove=%s", g.groveID)),
		),
	})
	if err != nil {
		return nil, fmt.Errorf("listing grove containers: %w", err)
	}

	seen := make(map[string]bool)
	var dirs []string

	for _, c := range containers {
		info, err := g.dockerClient.ContainerInspect(ctx, c.ID)
		if err != nil {
			if g.verbose {
				log.Printf("[grove] failed to inspect container %s: %v", c.ID[:12], err)
			}
			continue
		}

		for _, mount := range info.Mounts {
			if mount.Type != "bind" {
				continue
			}
			// Look for workspace mounts — the destination is typically /workspace.
			if mount.Destination == "/workspace" || mount.Destination == "/home/user/workspace" {
				hostPath := mount.Source
				if !seen[hostPath] {
					seen[hostPath] = true
					dirs = append(dirs, hostPath)
					if g.verbose {
						agentName := info.Config.Labels["scion.name"]
						log.Printf("[grove] discovered watch dir: %s (agent: %s)", hostPath, agentName)
					}
				}
			}
		}
	}

	if g.verbose {
		log.Printf("[grove] discovered %d directories for grove %q", len(dirs), g.groveID)
	}
	return dirs, nil
}

// DiscoverForContainer returns the workspace host directory for a specific container.
func (g *GroveDiscovery) DiscoverForContainer(ctx context.Context, containerID string) (string, error) {
	info, err := g.dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("inspecting container: %w", err)
	}

	// Check that the container belongs to our grove.
	if grove, ok := info.Config.Labels["scion.grove"]; !ok || grove != g.groveID {
		return "", nil
	}

	for _, mount := range info.Mounts {
		if mount.Type == "bind" && (mount.Destination == "/workspace" || mount.Destination == "/home/user/workspace") {
			return mount.Source, nil
		}
	}
	return "", nil
}
