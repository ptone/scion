package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"

	"github.com/GoogleCloudPlatform/scion/extras/agent-viz/internal/playback"
	"github.com/gorilla/websocket"
)

//go:embed dist/*
var embeddedAssets embed.FS

// Server serves the web frontend and handles WebSocket connections.
type Server struct {
	engine   *playback.Engine
	upgrader websocket.Upgrader
	clients  map[*wsClient]bool
	mu       sync.Mutex
}

type wsClient struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	closed bool
}

// PlaybackCommand is received from browser clients.
type PlaybackCommand struct {
	Type       string   `json:"type"`
	Timestamp  string   `json:"timestamp,omitempty"`
	Multiplier float64  `json:"multiplier,omitempty"`
	Agents     []string `json:"agents,omitempty"`
	EventTypes []string `json:"eventTypes,omitempty"`
	TimeRange  *struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"timeRange,omitempty"`
}

// New creates a new server.
func New(engine *playback.Engine) *Server {
	return &Server{
		engine:  engine,
		clients: make(map[*wsClient]bool),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Start begins serving on the given port.
func (s *Server) Start(port int, devMode bool) error {
	mux := http.NewServeMux()

	// WebSocket endpoint
	mux.HandleFunc("/ws", s.handleWebSocket)

	// Static file serving
	if devMode {
		// In dev mode, serve from web/dist directory on disk
		mux.Handle("/", http.FileServer(http.Dir("web/dist")))
	} else {
		// In production, serve from embedded assets
		distFS, err := fs.Sub(embeddedAssets, "dist")
		if err != nil {
			return fmt.Errorf("embedded assets: %w", err)
		}
		mux.Handle("/", http.FileServer(http.FS(distFS)))
	}

	// Start broadcasting events to clients
	go s.broadcastEvents()
	go s.broadcastStatus()
	go s.broadcastSnapshots()

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Agent Visualizer running at http://localhost:%d", port)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &wsClient{conn: conn}
	s.mu.Lock()
	s.clients[client] = true
	s.mu.Unlock()

	// Send manifest
	manifest := s.engine.Manifest()
	if err := client.writeJSON(manifest); err != nil {
		log.Printf("Error sending manifest: %v", err)
		s.removeClient(client)
		return
	}

	// Read commands from client
	go s.readCommands(client)
}

func (s *Server) readCommands(client *wsClient) {
	defer s.removeClient(client)
	for {
		_, msg, err := client.conn.ReadMessage()
		if err != nil {
			return
		}

		var cmd PlaybackCommand
		if err := json.Unmarshal(msg, &cmd); err != nil {
			log.Printf("Invalid command: %v", err)
			continue
		}

		s.handleCommand(cmd)
	}
}

func (s *Server) handleCommand(cmd PlaybackCommand) {
	switch cmd.Type {
	case "play":
		s.engine.Play()
	case "pause":
		s.engine.Pause()
	case "seek":
		s.engine.Seek(cmd.Timestamp)
	case "speed":
		s.engine.SetSpeed(cmd.Multiplier)
	case "filter":
		if cmd.Agents != nil {
			s.engine.SetAgentFilter(cmd.Agents)
		}
		if cmd.EventTypes != nil {
			s.engine.SetEventTypeFilter(cmd.EventTypes)
		}
		if cmd.TimeRange != nil {
			s.engine.SetTimeRange(cmd.TimeRange.Start, cmd.TimeRange.End)
		}
	}
}

func (s *Server) broadcastEvents() {
	for evt := range s.engine.Events() {
		s.broadcast(evt)
	}
}

func (s *Server) broadcastStatus() {
	for status := range s.engine.Status() {
		s.broadcast(status)
	}
}

func (s *Server) broadcastSnapshots() {
	for snapshot := range s.engine.Snapshots() {
		s.broadcast(snapshot)
	}
}

func (s *Server) broadcast(msg any) {
	s.mu.Lock()
	clients := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	for _, c := range clients {
		if err := c.writeJSON(msg); err != nil {
			s.removeClient(c)
		}
	}
}

func (s *Server) removeClient(client *wsClient) {
	client.mu.Lock()
	if !client.closed {
		client.closed = true
		client.conn.Close()
	}
	client.mu.Unlock()

	s.mu.Lock()
	delete(s.clients, client)
	s.mu.Unlock()
}

func (c *wsClient) writeJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("connection closed")
	}
	return c.conn.WriteJSON(v)
}
