package playback

import (
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/extras/agent-viz/internal/logparser"
)

// Engine controls playback timing, speed, seeking, and filtering.
type Engine struct {
	mu sync.Mutex

	events    []logparser.PlaybackEvent
	manifest  logparser.PlaybackManifest
	startTime time.Time
	endTime   time.Time

	// Playback state
	playing    bool
	speed      float64
	position   int // current event index
	currentTS  time.Time
	stopCh     chan struct{}
	eventsCh   chan logparser.PlaybackEvent
	statusCh   chan StatusUpdate
	snapshotCh chan SnapshotMessage

	// Filters
	agentFilter     map[string]bool // nil = all
	eventTypeFilter map[string]bool // nil = all
	timeRangeStart  time.Time
	timeRangeEnd    time.Time
}

type StatusUpdate struct {
	Type      string  `json:"type"`
	Playing   bool    `json:"playing"`
	Speed     float64 `json:"speed"`
	Position  int     `json:"position"`
	Total     int     `json:"total"`
	Timestamp string  `json:"timestamp"`
}

// SnapshotMessage contains all events up to a seek target for client state rebuild.
type SnapshotMessage struct {
	Type   string                    `json:"type"` // "snapshot"
	Events []logparser.PlaybackEvent `json:"events"`
}

// NewEngine creates a playback engine from parsed log data.
func NewEngine(result *logparser.ParseResult) (*Engine, error) {
	var startTime, endTime time.Time
	if result.Manifest.TimeRange.Start != "" {
		var err error
		startTime, err = logparser.TimestampToTime(result.Manifest.TimeRange.Start)
		if err != nil {
			return nil, err
		}
		endTime, err = logparser.TimestampToTime(result.Manifest.TimeRange.End)
		if err != nil {
			return nil, err
		}
	}

	return &Engine{
		events:         result.Events,
		manifest:       result.Manifest,
		startTime:      startTime,
		endTime:        endTime,
		speed:          1.0,
		currentTS:      startTime,
		timeRangeStart: startTime,
		timeRangeEnd:   endTime,
		eventsCh:       make(chan logparser.PlaybackEvent, 100),
		statusCh:       make(chan StatusUpdate, 10),
		snapshotCh:     make(chan SnapshotMessage, 1),
	}, nil
}

// Manifest returns the playback manifest.
func (e *Engine) Manifest() logparser.PlaybackManifest {
	return e.manifest
}

// Events returns the channel that emits playback events.
func (e *Engine) Events() <-chan logparser.PlaybackEvent {
	return e.eventsCh
}

// Status returns the channel that emits status updates.
func (e *Engine) Status() <-chan StatusUpdate {
	return e.statusCh
}

// Snapshots returns the channel that emits snapshot messages on seek.
func (e *Engine) Snapshots() <-chan SnapshotMessage {
	return e.snapshotCh
}

// Play starts or resumes playback.
func (e *Engine) Play() {
	e.mu.Lock()
	if e.playing {
		e.mu.Unlock()
		return
	}
	e.playing = true
	e.stopCh = make(chan struct{})
	e.mu.Unlock()

	e.sendStatus()
	go e.playbackLoop()
}

// Pause pauses playback.
func (e *Engine) Pause() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.playing {
		return
	}
	e.playing = false
	if e.stopCh != nil {
		close(e.stopCh)
		e.stopCh = nil
	}
	e.sendStatusLocked()
}

// Seek jumps to the event nearest the given timestamp and sends a snapshot.
func (e *Engine) Seek(timestamp string) {
	t, err := logparser.TimestampToTime(timestamp)
	if err != nil {
		return
	}

	e.mu.Lock()
	// Stop current playback
	wasPlaying := e.playing
	if e.playing && e.stopCh != nil {
		close(e.stopCh)
		e.stopCh = nil
	}
	e.playing = false

	// Find target position
	targetPos := 0
	for i, evt := range e.events {
		evtTime, err := logparser.TimestampToTime(evt.Timestamp)
		if err != nil {
			continue
		}
		if evtTime.After(t) {
			break
		}
		targetPos = i
	}

	// Build snapshot of all events up to target
	var snapshotEvents []logparser.PlaybackEvent
	for i := 0; i <= targetPos && i < len(e.events); i++ {
		if e.passesFilterLocked(e.events[i]) {
			snapshotEvents = append(snapshotEvents, e.events[i])
		}
	}

	e.position = targetPos
	e.currentTS = t
	e.sendStatusLocked()
	e.mu.Unlock()

	// Send snapshot (non-blocking)
	select {
	case e.snapshotCh <- SnapshotMessage{Type: "snapshot", Events: snapshotEvents}:
	default:
	}

	// Resume if was playing
	if wasPlaying {
		e.mu.Lock()
		e.playing = true
		e.stopCh = make(chan struct{})
		e.mu.Unlock()
		e.sendStatus()
		go e.playbackLoop()
	}
}

// SetSpeed sets the playback speed multiplier.
func (e *Engine) SetSpeed(multiplier float64) {
	if multiplier < 0.1 {
		multiplier = 0.1
	}
	if multiplier > 100 {
		multiplier = 100
	}
	e.mu.Lock()
	e.speed = multiplier
	e.mu.Unlock()
	e.sendStatus()
}

// SetAgentFilter sets which agents to include (nil = all).
func (e *Engine) SetAgentFilter(agents []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(agents) == 0 {
		e.agentFilter = nil
	} else {
		e.agentFilter = make(map[string]bool)
		for _, a := range agents {
			e.agentFilter[a] = true
		}
	}
}

// SetEventTypeFilter sets which event types to include (nil = all).
func (e *Engine) SetEventTypeFilter(types []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(types) == 0 {
		e.eventTypeFilter = nil
	} else {
		e.eventTypeFilter = make(map[string]bool)
		for _, t := range types {
			e.eventTypeFilter[t] = true
		}
	}
}

// SetTimeRange sets the playback time window.
func (e *Engine) SetTimeRange(start, end string) {
	s, err1 := logparser.TimestampToTime(start)
	en, err2 := logparser.TimestampToTime(end)
	if err1 != nil || err2 != nil {
		return
	}
	e.mu.Lock()
	e.timeRangeStart = s
	e.timeRangeEnd = en
	e.mu.Unlock()
}

// Close stops playback and closes channels.
func (e *Engine) Close() {
	e.mu.Lock()
	if e.playing && e.stopCh != nil {
		close(e.stopCh)
		e.stopCh = nil
	}
	e.playing = false
	e.mu.Unlock()
}

func (e *Engine) playbackLoop() {
	for {
		e.mu.Lock()
		if !e.playing || e.position >= len(e.events) {
			if e.position >= len(e.events) {
				e.playing = false
				e.sendStatusLocked()
			}
			e.mu.Unlock()
			return
		}

		evt := e.events[e.position]
		speed := e.speed
		stopCh := e.stopCh
		e.mu.Unlock()

		// Check time range
		evtTime, err := logparser.TimestampToTime(evt.Timestamp)
		if err != nil {
			e.mu.Lock()
			e.position++
			e.mu.Unlock()
			continue
		}

		e.mu.Lock()
		if evtTime.Before(e.timeRangeStart) {
			e.position++
			e.mu.Unlock()
			continue
		}
		if evtTime.After(e.timeRangeEnd) {
			e.playing = false
			e.sendStatusLocked()
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()

		// Check filters
		if !e.passesFilter(evt) {
			e.mu.Lock()
			e.position++
			e.mu.Unlock()
			continue
		}

		// Calculate delay to next event
		if e.position > 0 {
			prevTime, err := logparser.TimestampToTime(e.events[e.position-1].Timestamp)
			if err == nil {
				delay := evtTime.Sub(prevTime)
				scaledDelay := time.Duration(float64(delay) / speed)
				// Cap max delay at 2 seconds (real time)
				if scaledDelay > 2*time.Second {
					scaledDelay = 2 * time.Second
				}
				if scaledDelay > time.Millisecond {
					select {
					case <-time.After(scaledDelay):
					case <-stopCh:
						return
					}
				}
			}
		}

		// Emit event
		select {
		case e.eventsCh <- evt:
		case <-stopCh:
			return
		}

		e.mu.Lock()
		e.position++
		e.currentTS = evtTime
		e.sendStatusLocked()
		e.mu.Unlock()
	}
}

func (e *Engine) passesFilter(evt logparser.PlaybackEvent) bool {
	e.mu.Lock()
	result := e.passesFilterLocked(evt)
	e.mu.Unlock()
	return result
}

func (e *Engine) passesFilterLocked(evt logparser.PlaybackEvent) bool {
	if e.eventTypeFilter != nil && !e.eventTypeFilter[evt.Type] {
		return false
	}
	if e.agentFilter != nil {
		agentID := extractAgentID(evt)
		if agentID != "" && !e.agentFilter[agentID] {
			return false
		}
	}
	return true
}

func extractAgentID(evt logparser.PlaybackEvent) string {
	switch d := evt.Data.(type) {
	case logparser.AgentStateEvent:
		return d.AgentID
	case logparser.FileEditEvent:
		return d.AgentID
	case logparser.AgentLifecycleEvent:
		return d.AgentID
	case logparser.MessageEvent:
		return ""
	}
	return ""
}

func (e *Engine) sendStatus() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sendStatusLocked()
}

func (e *Engine) sendStatusLocked() {
	ts := e.currentTS.Format(time.RFC3339Nano)
	select {
	case e.statusCh <- StatusUpdate{
		Type:      "status",
		Playing:   e.playing,
		Speed:     e.speed,
		Position:  e.position,
		Total:     len(e.events),
		Timestamp: ts,
	}:
	default:
		// Don't block if channel is full
	}
}
