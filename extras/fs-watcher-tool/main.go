package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/GoogleCloudPlatform/scion/extras/fs-watcher-tool/pkg/fswatcher"
	"github.com/docker/docker/client"
)

type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	var (
		grove      string
		watchDirs  stringSlice
		logFile    string
		labelKey   string
		ignore     stringSlice
		filterFile string
		debounce   time.Duration
		cacheTTL   time.Duration
		verbose    bool
	)

	flag.StringVar(&grove, "grove", "", "Grove ID — auto-discover agent directories via Docker labels")
	flag.Var(&watchDirs, "watch", "Directory to watch explicitly (repeatable)")
	flag.StringVar(&logFile, "log", "-", "Output log file path (- for stdout)")
	flag.StringVar(&labelKey, "label-key", "scion.name", "Docker label key to use as agent ID")
	flag.Var(&ignore, "ignore", "Glob patterns to exclude (repeatable)")
	flag.StringVar(&filterFile, "filter-file", "", "Path to .gitignore-style filter file")
	flag.DurationVar(&debounce, "debounce", 300*time.Millisecond, "Duration to collapse rapid edits to the same file")
	flag.DurationVar(&cacheTTL, "cache-ttl", 5*time.Minute, "Duration to cache PID→container mappings")
	flag.BoolVar(&verbose, "verbose", false, "Enable debug logging to stderr")
	flag.Parse()

	if grove == "" && len(watchDirs) == 0 {
		fmt.Fprintln(os.Stderr, "error: at least one of --grove or --watch is required")
		flag.Usage()
		os.Exit(1)
	}

	// Default ignore pattern.
	if len(ignore) == 0 {
		ignore = stringSlice{".git/**"}
	}

	cfg := fswatcher.Config{
		Grove:      grove,
		WatchDirs:  watchDirs,
		LogFile:    logFile,
		LabelKey:   labelKey,
		Ignore:     ignore,
		FilterFile: filterFile,
		Debounce:   debounce,
		CacheTTL:   cacheTTL,
		Verbose:    verbose,
	}

	if err := run(cfg); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run(cfg fswatcher.Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Set up output.
	var out io.Writer
	if cfg.LogFile == "-" || cfg.LogFile == "" {
		out = os.Stdout
	} else {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("opening log file: %w", err)
		}
		defer f.Close()
		out = f
	}

	logger := fswatcher.NewLogger(out)

	// Set up filter.
	filter, err := fswatcher.NewFilter(cfg.Ignore, cfg.FilterFile)
	if err != nil {
		return fmt.Errorf("initializing filter: %w", err)
	}

	// Set up Docker client.
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("creating docker client: %w", err)
	}
	defer dockerClient.Close()

	// Set up resolver.
	resolver := fswatcher.NewResolver(dockerClient, cfg.LabelKey, cfg.CacheTTL, cfg.Verbose)
	if err := resolver.Warmup(ctx); err != nil {
		log.Printf("warning: resolver warmup failed: %v", err)
	}

	// Collect watch directories.
	roots := make([]string, len(cfg.WatchDirs))
	copy(roots, cfg.WatchDirs)

	var groveDiscovery *fswatcher.GroveDiscovery
	if cfg.Grove != "" {
		groveDiscovery = fswatcher.NewGroveDiscovery(dockerClient, cfg.Grove, cfg.Verbose)
		groveDirs, err := groveDiscovery.Discover(ctx)
		if err != nil {
			return fmt.Errorf("grove discovery: %w", err)
		}
		roots = append(roots, groveDirs...)
	}

	if len(roots) == 0 {
		return fmt.Errorf("no directories to watch (grove discovery found 0 directories and no --watch paths specified)")
	}

	// Build and start watcher.
	watcher := fswatcher.NewWatcher(cfg, roots, filter, resolver, logger)

	// Subscribe to container events for cache updates and dynamic grove discovery.
	var onStart func(string)
	if groveDiscovery != nil {
		onStart = func(containerID string) {
			dir, err := groveDiscovery.DiscoverForContainer(ctx, containerID)
			if err != nil || dir == "" {
				return
			}
			if err := watcher.AddRoot(dir); err != nil {
				log.Printf("warning: failed to add watch for new container dir %s: %v", dir, err)
			} else if cfg.Verbose {
				log.Printf("[main] added watch for new container dir: %s", dir)
			}
		}
	}
	resolver.WatchContainerEvents(ctx, onStart, nil)

	// Run event loop in a goroutine.
	errCh := make(chan error, 1)
	go func() {
		errCh <- watcher.Run(ctx)
	}()

	// Wait for signal or error.
	for {
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGHUP {
				log.Println("received SIGHUP, reloading filter file")
				if err := filter.Reload(cfg.Ignore, cfg.FilterFile); err != nil {
					log.Printf("warning: filter reload failed: %v", err)
				}
				continue
			}
			log.Printf("received %s, shutting down", sig)
			cancel()
			return <-errCh
		case err := <-errCh:
			return err
		}
	}
}
