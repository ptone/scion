package fswatcher

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// Resolver maps PIDs to agent IDs by correlating cgroup → container → Docker label.
type Resolver struct {
	mu             sync.RWMutex
	containerCache map[string]string // containerID → agentID
	pidCache       map[int]pidEntry  // pid → containerID (with expiry)

	dockerClient *client.Client
	labelKey     string
	cacheTTL     time.Duration
	verbose      bool
}

type pidEntry struct {
	containerID string
	expiresAt   time.Time
}

// NewResolver creates a Resolver that uses the Docker API for container label lookups.
func NewResolver(dockerClient *client.Client, labelKey string, cacheTTL time.Duration, verbose bool) *Resolver {
	return &Resolver{
		containerCache: make(map[string]string),
		pidCache:       make(map[int]pidEntry),
		dockerClient:   dockerClient,
		labelKey:       labelKey,
		cacheTTL:       cacheTTL,
		verbose:        verbose,
	}
}

// Warmup pre-populates the container cache with all running containers that have the label key.
func (r *Resolver) Warmup(ctx context.Context) error {
	containers, err := r.dockerClient.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", r.labelKey)),
	})
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range containers {
		if agentID, ok := c.Labels[r.labelKey]; ok {
			r.containerCache[c.ID] = agentID
			// Also cache short ID prefix.
			if len(c.ID) >= 12 {
				r.containerCache[c.ID[:12]] = agentID
			}
		}
	}
	if r.verbose {
		log.Printf("[resolver] warmed up with %d containers", len(containers))
	}
	return nil
}

// WatchContainerEvents subscribes to Docker start/die events and updates the cache.
func (r *Resolver) WatchContainerEvents(ctx context.Context, onStart func(containerID string), onDie func(containerID string)) {
	eventsCh, errCh := r.dockerClient.Events(ctx, events.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("type", "container"),
			filters.Arg("event", "start"),
			filters.Arg("event", "die"),
		),
	})

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-eventsCh:
				switch ev.Action {
				case events.ActionStart:
					r.handleContainerStart(ctx, ev.Actor.ID, onStart)
				case events.ActionDie:
					r.handleContainerDie(ev.Actor.ID, onDie)
				}
			case err := <-errCh:
				if err != nil && ctx.Err() == nil {
					log.Printf("[resolver] docker events error: %v", err)
				}
				return
			}
		}
	}()
}

func (r *Resolver) handleContainerStart(ctx context.Context, containerID string, onStart func(string)) {
	info, err := r.dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		if r.verbose {
			log.Printf("[resolver] failed to inspect container %s: %v", containerID[:12], err)
		}
		return
	}
	agentID, ok := info.Config.Labels[r.labelKey]
	if !ok {
		return
	}

	r.mu.Lock()
	r.containerCache[containerID] = agentID
	if len(containerID) >= 12 {
		r.containerCache[containerID[:12]] = agentID
	}
	r.mu.Unlock()

	if r.verbose {
		log.Printf("[resolver] container started: %s → %s", containerID[:12], agentID)
	}
	if onStart != nil {
		onStart(containerID)
	}
}

func (r *Resolver) handleContainerDie(containerID string, onDie func(string)) {
	r.mu.Lock()
	delete(r.containerCache, containerID)
	if len(containerID) >= 12 {
		delete(r.containerCache, containerID[:12])
	}
	r.mu.Unlock()

	if r.verbose {
		log.Printf("[resolver] container died: %s", containerID[:12])
	}
	if onDie != nil {
		onDie(containerID)
	}
}

// Resolve maps a PID to an agent ID string. Returns "" if unresolvable.
func (r *Resolver) Resolve(pid int) string {
	// Check PID cache first.
	r.mu.RLock()
	if entry, ok := r.pidCache[pid]; ok && time.Now().Before(entry.expiresAt) {
		cid := entry.containerID
		agentID := r.containerCache[cid]
		r.mu.RUnlock()
		return agentID
	}
	r.mu.RUnlock()

	// Resolve PID → container ID via cgroup.
	containerID := resolveContainerFromCgroup(pid)
	if containerID == "" {
		return ""
	}

	r.mu.Lock()
	r.pidCache[pid] = pidEntry{
		containerID: containerID,
		expiresAt:   time.Now().Add(r.cacheTTL),
	}
	agentID := r.containerCache[containerID]
	// Try short ID if full didn't match.
	if agentID == "" && len(containerID) >= 12 {
		agentID = r.containerCache[containerID[:12]]
	}
	r.mu.Unlock()

	// If not in cache, try to inspect the container.
	if agentID == "" {
		agentID = r.inspectAndCache(containerID)
	}

	return agentID
}

func (r *Resolver) inspectAndCache(containerID string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	info, err := r.dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		return ""
	}
	agentID, ok := info.Config.Labels[r.labelKey]
	if !ok {
		return ""
	}

	r.mu.Lock()
	r.containerCache[containerID] = agentID
	if len(containerID) >= 12 {
		r.containerCache[containerID[:12]] = agentID
	}
	r.mu.Unlock()

	return agentID
}

// resolveContainerFromCgroup reads /proc/<pid>/cgroup to extract a Docker container ID.
func resolveContainerFromCgroup(pid int) string {
	path := fmt.Sprintf("/proc/%d/cgroup", pid)
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// cgroup v2: "0::/system.slice/docker-<id>.scope"
		// cgroup v1: "...:/.../docker/<id>"
		if id := extractContainerID(line); id != "" {
			return id
		}
	}

	// Fallback: try /proc/<pid>/cpuset
	return resolveContainerFromCpuset(pid)
}

func resolveContainerFromCpuset(pid int) string {
	path := fmt.Sprintf("/proc/%d/cpuset", pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return extractContainerID(string(data))
}

// extractContainerID looks for a 64-char hex container ID in a cgroup/cpuset line.
func extractContainerID(line string) string {
	// Pattern: "docker-<64hex>.scope"
	if idx := strings.Index(line, "docker-"); idx != -1 {
		rest := line[idx+7:]
		if dotIdx := strings.Index(rest, ".scope"); dotIdx == 64 {
			return rest[:64]
		}
	}
	// Pattern: "docker/<64hex>"
	if idx := strings.Index(line, "docker/"); idx != -1 {
		rest := line[idx+7:]
		if len(rest) >= 64 && isHex(rest[:64]) {
			return rest[:64]
		}
	}
	// Pattern: containerd "cri-containerd-<64hex>.scope"
	if idx := strings.Index(line, "cri-containerd-"); idx != -1 {
		rest := line[idx+15:]
		if dotIdx := strings.Index(rest, ".scope"); dotIdx == 64 {
			return rest[:64]
		}
	}
	return ""
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
