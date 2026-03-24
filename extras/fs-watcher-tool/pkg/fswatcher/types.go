package fswatcher

import "time"

// Action represents the type of filesystem event.
type Action string

const (
	ActionCreate     Action = "create"
	ActionModify     Action = "modify"
	ActionDelete     Action = "delete"
	ActionRenameFrom Action = "rename_from"
	ActionRenameTo   Action = "rename_to"
)

// Event is a single filesystem event attributed to an agent.
type Event struct {
	Timestamp time.Time `json:"ts"`
	AgentID   string    `json:"agent_id"`
	Action    Action    `json:"action"`
	Path      string    `json:"path"`
	Size      *int64    `json:"size,omitempty"`
}

// Config holds the watcher configuration parsed from CLI flags.
type Config struct {
	Grove      string
	WatchDirs  []string
	LogFile    string
	LabelKey   string
	Ignore     []string
	FilterFile string
	Debounce   time.Duration
	CacheTTL   time.Duration
	Verbose    bool
}
