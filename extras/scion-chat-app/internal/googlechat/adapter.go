// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package googlechat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/chatapp"
)

const (
	PlatformName = "google_chat"
	chatAPIBase  = "https://chat.googleapis.com/v1"
)

// EventHandler processes normalized chat events.
type EventHandler func(ctx context.Context, event *chatapp.ChatEvent) error

// Adapter implements the chatapp.Messenger interface for Google Chat.
type Adapter struct {
	projectID    string
	audience     string
	httpServer   *http.Server
	eventHandler EventHandler
	httpClient   *http.Client // authenticated client for Chat API calls
	log          *slog.Logger

	mu     sync.RWMutex
	spaces map[string]bool // tracked spaces
}

// Config holds Google Chat adapter configuration.
type Config struct {
	ProjectID     string
	Audience      string
	ListenAddress string
	Credentials   string // Path to service account key
}

// NewAdapter creates a new Google Chat adapter.
func NewAdapter(cfg Config, handler EventHandler, httpClient *http.Client, log *slog.Logger) *Adapter {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Adapter{
		projectID:    cfg.ProjectID,
		audience:     cfg.Audience,
		eventHandler: handler,
		httpClient:   httpClient,
		log:          log,
		spaces:       make(map[string]bool),
	}
}

// Start begins serving the HTTP webhook endpoint for Google Chat events.
func (a *Adapter) Start(listenAddr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", a.handleEvent)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	a.httpServer = &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	a.log.Info("google chat webhook server starting", "address", listenAddr)
	return a.httpServer.ListenAndServe()
}

// Stop gracefully shuts down the webhook server.
func (a *Adapter) Stop(ctx context.Context) error {
	if a.httpServer != nil {
		return a.httpServer.Shutdown(ctx)
	}
	return nil
}

// handleEvent processes incoming Google Chat webhook events.
func (a *Adapter) handleEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.log.Error("reading event body", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var raw rawEvent
	if err := json.Unmarshal(body, &raw); err != nil {
		a.log.Error("parsing event", "error", err)
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	event := a.normalizeEvent(&raw)
	if event == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := a.eventHandler(r.Context(), event); err != nil {
		a.log.Error("handling event", "type", event.Type, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// rawEvent represents the raw Google Chat event payload.
type rawEvent struct {
	Type         string       `json:"type"`
	EventTime    string       `json:"eventTime"`
	Space        rawSpace     `json:"space"`
	Message      *rawMessage  `json:"message,omitempty"`
	User         rawUser      `json:"user"`
	Action       *rawAction   `json:"action,omitempty"`
	Common       *rawCommon   `json:"common,omitempty"`
	SlashCommand *rawSlashCmd `json:"slashCommand,omitempty"`
}

type rawSpace struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

type rawMessage struct {
	Name         string          `json:"name"`
	Text         string          `json:"text"`
	ArgumentText string          `json:"argumentText"`
	Thread       *rawThread      `json:"thread,omitempty"`
	SlashCommand *rawSlashCmd    `json:"slashCommand,omitempty"`
	Annotations  []rawAnnotation `json:"annotations,omitempty"`
}

type rawThread struct {
	Name      string `json:"name"`
	ThreadKey string `json:"threadKey,omitempty"`
}

type rawSlashCmd struct {
	CommandID int64 `json:"commandId"`
}

type rawAnnotation struct {
	Type       string `json:"type"`
	StartIndex int    `json:"startIndex"`
	Length     int    `json:"length"`
}

type rawUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Type        string `json:"type"`
}

type rawAction struct {
	ActionMethodName string           `json:"actionMethodName"`
	Parameters       []rawActionParam `json:"parameters"`
}

type rawActionParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type rawCommon struct {
	FormInputs map[string]rawFormInput `json:"formInputs,omitempty"`
}

type rawFormInput struct {
	StringInputs *rawStringInputs `json:"stringInputs,omitempty"`
}

type rawStringInputs struct {
	Value []string `json:"value"`
}

// normalizeEvent converts a raw Google Chat event to a ChatEvent.
func (a *Adapter) normalizeEvent(raw *rawEvent) *chatapp.ChatEvent {
	event := &chatapp.ChatEvent{
		Platform: PlatformName,
		SpaceID:  raw.Space.Name,
		UserID:   raw.User.Name,
	}

	switch raw.Type {
	case "ADDED_TO_SPACE":
		event.Type = chatapp.EventSpaceJoin
		a.mu.Lock()
		a.spaces[raw.Space.Name] = true
		a.mu.Unlock()
		return event

	case "REMOVED_FROM_SPACE":
		event.Type = chatapp.EventSpaceRemove
		a.mu.Lock()
		delete(a.spaces, raw.Space.Name)
		a.mu.Unlock()
		return event

	case "MESSAGE":
		if raw.Message == nil {
			return nil
		}
		if raw.Message.Thread != nil {
			event.ThreadID = raw.Message.Thread.Name
		}

		// Check for slash command
		if raw.Message.SlashCommand != nil {
			event.Type = chatapp.EventCommand
			event.Command = "scion"
			event.Args = strings.TrimSpace(raw.Message.ArgumentText)
			return event
		}

		// Regular message (with @mention stripped via ArgumentText)
		event.Type = chatapp.EventMessage
		text := raw.Message.ArgumentText
		if text == "" {
			text = raw.Message.Text
		}
		event.Text = strings.TrimSpace(text)
		return event

	case "CARD_CLICKED":
		if raw.Message != nil && raw.Message.Thread != nil {
			event.ThreadID = raw.Message.Thread.Name
		}
		if raw.Action != nil {
			event.Type = chatapp.EventAction
			event.ActionID = raw.Action.ActionMethodName
			// Collect action parameters
			for _, p := range raw.Action.Parameters {
				if event.ActionData != "" {
					event.ActionData += ","
				}
				event.ActionData += p.Key + "=" + p.Value
			}
		}

		// Check for dialog/form submission
		if raw.Common != nil && len(raw.Common.FormInputs) > 0 {
			event.Type = chatapp.EventDialogSubmit
			event.DialogData = make(map[string]string)
			for k, v := range raw.Common.FormInputs {
				if v.StringInputs != nil && len(v.StringInputs.Value) > 0 {
					event.DialogData[k] = v.StringInputs.Value[0]
				}
			}
		}
		return event

	default:
		a.log.Debug("unhandled event type", "type", raw.Type)
		return nil
	}
}

// SendMessage sends a text or card message to a Google Chat space.
func (a *Adapter) SendMessage(ctx context.Context, req chatapp.SendMessageRequest) (string, error) {
	payload := map[string]any{}

	if req.Text != "" {
		payload["text"] = req.Text
	}

	if req.Card != nil {
		payload["cardsV2"] = []map[string]any{
			{
				"cardId": "scion_card",
				"card":   renderCardV2(req.Card),
			},
		}
	}

	if req.ThreadID != "" {
		payload["thread"] = map[string]string{
			"name": req.ThreadID,
		}
	}

	url := fmt.Sprintf("%s/%s/messages", chatAPIBase, req.SpaceID)
	if req.ThreadID != "" {
		url += "?messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"
	}

	respBody, err := a.doPost(ctx, url, payload)
	if err != nil {
		return "", fmt.Errorf("sending message: %w", err)
	}

	var result struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}
	return result.Name, nil
}

// SendCard sends a card-only message to a Google Chat space.
func (a *Adapter) SendCard(ctx context.Context, spaceID string, card chatapp.Card) (string, error) {
	return a.SendMessage(ctx, chatapp.SendMessageRequest{
		SpaceID: spaceID,
		Card:    &card,
	})
}

// UpdateMessage updates an existing message.
func (a *Adapter) UpdateMessage(ctx context.Context, messageID string, req chatapp.SendMessageRequest) error {
	payload := map[string]any{}
	if req.Text != "" {
		payload["text"] = req.Text
	}
	if req.Card != nil {
		payload["cardsV2"] = []map[string]any{
			{
				"cardId": "scion_card",
				"card":   renderCardV2(req.Card),
			},
		}
	}

	url := fmt.Sprintf("%s/%s", chatAPIBase, messageID)
	_, err := a.doPatch(ctx, url, payload)
	return err
}

// OpenDialog presents a dialog in Google Chat.
func (a *Adapter) OpenDialog(ctx context.Context, triggerID string, dialog chatapp.Dialog) error {
	// Google Chat dialogs are returned as part of the webhook response,
	// not as separate API calls. This is handled in the event handler.
	a.log.Debug("dialog open requested (handled via webhook response)", "trigger", triggerID)
	return nil
}

// UpdateDialog updates an existing dialog.
func (a *Adapter) UpdateDialog(ctx context.Context, triggerID string, dialog chatapp.Dialog) error {
	a.log.Debug("dialog update requested", "trigger", triggerID)
	return nil
}

// GetUser retrieves a Google Chat user's information.
func (a *Adapter) GetUser(ctx context.Context, userID string) (*chatapp.ChatUser, error) {
	// Google Chat provides user info in webhook events;
	// for standalone lookups, we use the People API or cached data.
	// For MVP, return a placeholder with the user ID.
	return &chatapp.ChatUser{
		PlatformID: userID,
	}, nil
}

// SetAgentIdentity configures how agent messages appear.
// In Google Chat, the bot identity is fixed; we use card headers instead.
func (a *Adapter) SetAgentIdentity(ctx context.Context, agent chatapp.AgentIdentity) error {
	a.log.Debug("agent identity set (used in card headers)", "slug", agent.Slug)
	return nil
}

// renderCardV2 converts a platform-agnostic Card to Google Chat Cards V2 format.
func renderCardV2(card *chatapp.Card) map[string]any {
	c := map[string]any{}

	// Header
	if card.Header.Title != "" {
		header := map[string]any{
			"title": card.Header.Title,
		}
		if card.Header.Subtitle != "" {
			header["subtitle"] = card.Header.Subtitle
		}
		if card.Header.IconURL != "" {
			header["imageUrl"] = card.Header.IconURL
			header["imageType"] = "CIRCLE"
		}
		c["header"] = header
	}

	// Sections
	sections := make([]map[string]any, 0)
	for _, s := range card.Sections {
		section := map[string]any{}
		if s.Header != "" {
			section["header"] = s.Header
		}
		if len(s.Widgets) > 0 {
			widgets := make([]map[string]any, 0, len(s.Widgets))
			for _, w := range s.Widgets {
				widget := renderWidget(&w)
				if widget != nil {
					widgets = append(widgets, widget)
				}
			}
			section["widgets"] = widgets
		}
		sections = append(sections, section)
	}

	// Render card-level actions as a button list in a footer section
	if len(card.Actions) > 0 {
		buttons := make([]any, 0, len(card.Actions))
		for _, a := range card.Actions {
			btn := map[string]any{
				"text": a.Label,
				"onClick": map[string]any{
					"action": map[string]any{
						"function": a.ActionID,
					},
				},
			}
			if a.Style == "danger" {
				btn["color"] = map[string]any{
					"red": 0.9, "green": 0.2, "blue": 0.2, "alpha": 1,
				}
			} else if a.Style == "primary" {
				btn["color"] = map[string]any{
					"red": 0.1, "green": 0.5, "blue": 0.9, "alpha": 1,
				}
			}
			buttons = append(buttons, btn)
		}
		sections = append(sections, map[string]any{
			"widgets": []any{
				map[string]any{
					"buttonList": map[string]any{
						"buttons": buttons,
					},
				},
			},
		})
	}

	if len(sections) > 0 {
		c["sections"] = sections
	}

	return c
}

// renderWidget converts a Widget to Google Chat widget format.
func renderWidget(w *chatapp.Widget) map[string]any {
	switch w.Type {
	case chatapp.WidgetText:
		return map[string]any{
			"textParagraph": map[string]any{
				"text": w.Content,
			},
		}
	case chatapp.WidgetKeyValue:
		decorated := map[string]any{
			"topLabel": w.Label,
			"text":     w.Content,
		}
		return map[string]any{
			"decoratedText": decorated,
		}
	case chatapp.WidgetButton:
		btn := map[string]any{
			"text": w.Label,
			"onClick": map[string]any{
				"action": map[string]any{
					"function": w.ActionID,
				},
			},
		}
		return map[string]any{
			"buttonList": map[string]any{
				"buttons": []any{btn},
			},
		}
	case chatapp.WidgetDivider:
		return map[string]any{
			"divider": map[string]any{},
		}
	case chatapp.WidgetImage:
		return map[string]any{
			"image": map[string]any{
				"imageUrl": w.Content,
			},
		}
	case chatapp.WidgetInput:
		input := map[string]any{
			"label": w.Label,
			"name":  w.ActionID,
			"type":  "SINGLE_LINE",
		}
		if w.ActionID != "" {
			input["onChangeAction"] = map[string]any{
				"action": map[string]any{
					"function": w.ActionID,
				},
			}
		}
		return map[string]any{
			"textInput": input,
		}
	case chatapp.WidgetCheckbox:
		items := make([]any, 0, len(w.Options))
		for _, opt := range w.Options {
			items = append(items, map[string]any{
				"text":     opt.Label,
				"value":    opt.Value,
				"selected": false,
			})
		}
		return map[string]any{
			"selectionInput": map[string]any{
				"name":  w.ActionID,
				"label": w.Label,
				"type":  "CHECK_BOX",
				"items": items,
			},
		}
	default:
		return nil
	}
}

// doPost performs an authenticated POST request.
func (a *Adapter) doPost(ctx context.Context, url string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("chat API error %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// doPatch performs an authenticated PATCH request.
func (a *Adapter) doPatch(ctx context.Context, url string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("chat API error %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
