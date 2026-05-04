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

package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/chatapp"
	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/identity"
	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/state"
)

const PlatformName = "slack"

// ephemeralCommands are slash command subcommands whose responses should be
// visible only to the invoker.
var ephemeralCommands = map[string]bool{
	"help":       true,
	"info":       true,
	"register":   true,
	"unregister": true,
}

// EventHandler processes normalized chat events.
type EventHandler func(ctx context.Context, event *chatapp.ChatEvent) (*chatapp.EventResponse, error)

// IconProvider generates avatar URLs for agents.
type IconProvider interface {
	IconURL(agentSlug string) string
}

// robohashProvider generates deterministic robot-themed avatars via robohash.org.
type robohashProvider struct{}

func (r *robohashProvider) IconURL(agentSlug string) string {
	return fmt.Sprintf("https://robohash.org/%s?set=set1&size=48x48", url.PathEscape(agentSlug))
}

// Config holds Slack adapter configuration.
type Config struct {
	BotToken      string
	AppToken      string
	SigningSecret string
	ListenAddress string
	SocketMode    bool
}

// Adapter implements chatapp.Messenger for Slack.
type Adapter struct {
	client        *slackapi.Client
	socketClient  *socketmode.Client
	botToken      string
	signingSecret string
	appToken      string
	httpServer    *http.Server
	handler       EventHandler
	iconProvider  IconProvider
	log           *slog.Logger
	botUserID     string

	store    *state.Store
	idMapper *identity.Mapper

	cacheMu    sync.RWMutex
	userCache  map[string]*cachedUser
}

type cachedUser struct {
	user      *chatapp.ChatUser
	fetchedAt time.Time
}

const userCacheTTL = 15 * time.Minute
const asyncProcessingTimeout = 30 * time.Second

// NewAdapter creates a new Slack adapter.
func NewAdapter(cfg Config, handler EventHandler, log *slog.Logger) *Adapter {
	client := slackapi.New(cfg.BotToken)

	a := &Adapter{
		client:        client,
		botToken:      cfg.BotToken,
		signingSecret: cfg.SigningSecret,
		appToken:      cfg.AppToken,
		handler:       handler,
		iconProvider:  &robohashProvider{},
		log:           log,
		userCache:     make(map[string]*cachedUser),
	}

	// Resolve bot user ID for mention stripping.
	resp, err := client.AuthTest()
	if err != nil {
		log.Warn("failed to resolve bot user ID via auth.test", "error", err)
	} else {
		a.botUserID = resp.UserID
		log.Info("slack bot user ID resolved", "bot_user_id", a.botUserID)
	}

	return a
}

// SetIconProvider overrides the default icon provider.
func (a *Adapter) SetIconProvider(p IconProvider) {
	a.iconProvider = p
}

// SetStore sets the state store for App Home rendering.
func (a *Adapter) SetStore(store *state.Store) {
	a.store = store
}

// SetIdentityMapper sets the identity mapper for App Home rendering.
func (a *Adapter) SetIdentityMapper(m *identity.Mapper) {
	a.idMapper = m
}

// --- Messenger interface implementation ---

func (a *Adapter) SendMessage(ctx context.Context, req chatapp.SendMessageRequest) (string, error) {
	opts := []slackapi.MsgOption{
		slackapi.MsgOptionText(req.Text, false),
	}

	if req.AgentID != "" {
		opts = append(opts,
			slackapi.MsgOptionUsername(req.AgentID),
			slackapi.MsgOptionIconURL(a.iconProvider.IconURL(req.AgentID)),
		)
	}

	if req.ThreadID != "" {
		opts = append(opts, slackapi.MsgOptionTS(req.ThreadID))
	}

	if req.Card != nil {
		blocks := renderBlocks(req.Card)
		opts = append(opts, slackapi.MsgOptionBlocks(blocks...))
	}

	_, ts, err := a.client.PostMessageContext(ctx, req.SpaceID, opts...)
	if err != nil {
		a.log.Error("failed to send message", "space", req.SpaceID, "error", err)
		return "", err
	}
	return ts, nil
}

func (a *Adapter) SendCard(ctx context.Context, spaceID string, card chatapp.Card) (string, error) {
	return a.SendMessage(ctx, chatapp.SendMessageRequest{
		SpaceID: spaceID,
		Card:    &card,
	})
}

func (a *Adapter) UpdateMessage(ctx context.Context, messageID string, req chatapp.SendMessageRequest) error {
	opts := []slackapi.MsgOption{
		slackapi.MsgOptionText(req.Text, false),
	}
	if req.Card != nil {
		blocks := renderBlocks(req.Card)
		opts = append(opts, slackapi.MsgOptionBlocks(blocks...))
	}

	_, _, _, err := a.client.UpdateMessageContext(ctx, req.SpaceID, messageID, opts...)
	return err
}

func (a *Adapter) OpenDialog(ctx context.Context, triggerID string, dialog chatapp.Dialog) error {
	view := renderModal(&dialog)
	_, err := a.client.OpenViewContext(ctx, triggerID, view)
	return err
}

func (a *Adapter) UpdateDialog(ctx context.Context, viewID string, dialog chatapp.Dialog) error {
	view := renderModal(&dialog)
	_, err := a.client.UpdateViewContext(ctx, view, "", "", viewID)
	return err
}

func (a *Adapter) GetUser(ctx context.Context, userID string) (*chatapp.ChatUser, error) {
	if cached := a.getCachedUser(userID); cached != nil {
		return cached, nil
	}

	user, err := a.client.GetUserInfoContext(ctx, userID)
	if err != nil {
		return nil, err
	}

	chatUser := &chatapp.ChatUser{
		PlatformID:  user.ID,
		DisplayName: user.Profile.DisplayName,
		Email:       user.Profile.Email,
	}
	a.setCachedUser(userID, chatUser)
	return chatUser, nil
}

func (a *Adapter) SetAgentIdentity(_ context.Context, _ chatapp.AgentIdentity) error {
	return nil
}

// --- User cache ---

func (a *Adapter) getCachedUser(userID string) *chatapp.ChatUser {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if cached, ok := a.userCache[userID]; ok && time.Since(cached.fetchedAt) < userCacheTTL {
		return cached.user
	}
	return nil
}

func (a *Adapter) setCachedUser(userID string, user *chatapp.ChatUser) {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	a.userCache[userID] = &cachedUser{user: user, fetchedAt: time.Now()}
}

// --- HTTP server ---

// Start begins serving the HTTP webhook endpoints for Slack events.
func (a *Adapter) Start(listenAddr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /slack/events", a.handleEvents)
	mux.HandleFunc("POST /slack/commands", a.handleCommands)
	mux.HandleFunc("POST /slack/interactions", a.handleInteractions)
	mux.HandleFunc("GET /slack/healthz", a.handleHealthz)

	a.httpServer = &http.Server{Addr: listenAddr, Handler: mux}
	a.log.Info("slack webhook server starting", "address", listenAddr)
	return a.httpServer.ListenAndServe()
}

// StartSocketMode starts a WebSocket connection to Slack instead of an HTTP server.
func (a *Adapter) StartSocketMode() error {
	appClient := slackapi.New(a.botToken, slackapi.OptionAppLevelToken(a.appToken))
	a.socketClient = socketmode.New(appClient)

	go func() {
		for evt := range a.socketClient.Events {
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				a.handleSocketEvent(evt)
			case socketmode.EventTypeSlashCommand:
				a.handleSocketCommand(evt)
			case socketmode.EventTypeInteractive:
				a.handleSocketInteraction(evt)
			}
		}
	}()

	a.log.Info("slack socket mode starting")
	return a.socketClient.Run()
}

// Stop gracefully shuts down the adapter.
func (a *Adapter) Stop(ctx context.Context) error {
	if a.httpServer != nil {
		return a.httpServer.Shutdown(ctx)
	}
	return nil
}

// --- HTTP handlers ---

func (a *Adapter) handleEvents(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if a.signingSecret != "" {
		if err := verifyRequest(r.Header, body, a.signingSecret); err != nil {
			a.log.Warn("event signature verification failed", "error", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	eventsAPIEvent, err := slackevents.ParseEvent(json.RawMessage(body), slackevents.OptionNoVerifyToken())
	if err != nil {
		a.log.Error("failed to parse event", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Handle URL verification challenge.
	if eventsAPIEvent.Type == slackevents.URLVerification {
		var challenge slackevents.ChallengeResponse
		if err := json.Unmarshal(body, &challenge); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, challenge.Challenge)
		return
	}

	// Acknowledge immediately.
	w.WriteHeader(http.StatusOK)

	// Process asynchronously.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), asyncProcessingTimeout)
		defer cancel()
		a.processEventsAPIEvent(ctx, &eventsAPIEvent)
	}()
}

func (a *Adapter) handleCommands(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if a.signingSecret != "" {
		if err := verifyRequest(r.Header, body, a.signingSecret); err != nil {
			a.log.Warn("command signature verification failed", "error", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Restore body for SlashCommandParse.
	r.Body = io.NopCloser(bytes.NewReader(body))
	cmd, err := slackapi.SlashCommandParse(r)
	if err != nil {
		a.log.Error("failed to parse slash command", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Acknowledge immediately with an ephemeral "Processing..." message.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"response_type": "ephemeral",
		"text":          "Processing...",
	})

	// Process asynchronously.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), asyncProcessingTimeout)
		defer cancel()
		a.processSlashCommand(ctx, cmd)
	}()
}

func (a *Adapter) handleInteractions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if a.signingSecret != "" {
		if err := verifyRequest(r.Header, body, a.signingSecret); err != nil {
			a.log.Warn("interaction signature verification failed", "error", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Parse the payload from form data.
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	payloadJSON := r.FormValue("payload")
	if payloadJSON == "" {
		http.Error(w, "missing payload", http.StatusBadRequest)
		return
	}

	var callback slackapi.InteractionCallback
	if err := json.Unmarshal([]byte(payloadJSON), &callback); err != nil {
		a.log.Error("failed to parse interaction payload", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Acknowledge immediately.
	w.WriteHeader(http.StatusOK)

	// Process asynchronously.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), asyncProcessingTimeout)
		defer cancel()
		a.processInteraction(ctx, callback)
	}()
}

func (a *Adapter) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

// --- Socket Mode handlers ---

func (a *Adapter) handleSocketEvent(evt socketmode.Event) {
	data, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return
	}
	a.socketClient.Ack(*evt.Request)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), asyncProcessingTimeout)
		defer cancel()
		a.processEventsAPIEvent(ctx, &data)
	}()
}

func (a *Adapter) handleSocketCommand(evt socketmode.Event) {
	cmd, ok := evt.Data.(slackapi.SlashCommand)
	if !ok {
		return
	}
	a.socketClient.Ack(*evt.Request)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), asyncProcessingTimeout)
		defer cancel()
		a.processSlashCommand(ctx, cmd)
	}()
}

func (a *Adapter) handleSocketInteraction(evt socketmode.Event) {
	callback, ok := evt.Data.(slackapi.InteractionCallback)
	if !ok {
		return
	}
	a.socketClient.Ack(*evt.Request)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), asyncProcessingTimeout)
		defer cancel()
		a.processInteraction(ctx, callback)
	}()
}

// --- Event processing ---

func (a *Adapter) processEventsAPIEvent(ctx context.Context, evt *slackevents.EventsAPIEvent) {
	if evt.Type != slackevents.CallbackEvent {
		return
	}

	var chatEvent *chatapp.ChatEvent

	switch inner := evt.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		chatEvent = normalizeAppMention(inner)

	case *slackevents.MessageEvent:
		if inner.BotID != "" || inner.SubType != "" {
			return
		}
		chatEvent = normalizeMessageIM(inner)

	case *slackevents.MemberJoinedChannelEvent:
		if a.botUserID != "" && inner.User != a.botUserID {
			return
		}
		chatEvent = normalizeMemberJoined(inner)

	case *slackevents.MemberLeftChannelEvent:
		if a.botUserID != "" && inner.User != a.botUserID {
			return
		}
		chatEvent = normalizeMemberLeft(inner)

	case *slackevents.AppHomeOpenedEvent:
		a.publishAppHome(ctx, inner.User)
		return

	default:
		a.log.Debug("unhandled inner event type", "type", evt.InnerEvent.Type)
		return
	}

	if chatEvent == nil {
		return
	}

	a.log.Info("event received",
		"type", chatEvent.Type,
		"space", chatEvent.SpaceID,
		"user", chatEvent.UserID,
	)

	resp, err := a.handler(ctx, chatEvent)
	if err != nil {
		a.log.Error("event handler error", "type", chatEvent.Type, "error", err)
		return
	}

	a.sendResponse(ctx, chatEvent.SpaceID, chatEvent.ThreadID, "", resp)
}

func (a *Adapter) processSlashCommand(ctx context.Context, cmd slackapi.SlashCommand) {
	event := normalizeSlashCommand(cmd)

	a.log.Info("slash command received",
		"text", cmd.Text,
		"channel", cmd.ChannelID,
		"user", cmd.UserID,
	)

	resp, err := a.handler(ctx, event)
	if err != nil {
		a.log.Error("command handler error", "error", err)
		return
	}

	if resp == nil || resp.Message == nil {
		return
	}

	// Determine if this should be an ephemeral response.
	subcommand := strings.Fields(cmd.Text)
	if len(subcommand) > 0 && ephemeralCommands[strings.ToLower(subcommand[0])] {
		a.sendEphemeral(ctx, cmd.ChannelID, cmd.UserID, resp)
		return
	}

	a.sendResponse(ctx, cmd.ChannelID, "", "", resp)
}

func (a *Adapter) processInteraction(ctx context.Context, callback slackapi.InteractionCallback) {
	event := normalizeInteraction(callback)
	if event == nil {
		return
	}

	a.log.Info("interaction received",
		"type", callback.Type,
		"action_id", event.ActionID,
		"user", event.UserID,
	)

	resp, err := a.handler(ctx, event)
	if err != nil {
		a.log.Error("interaction handler error", "error", err)
		return
	}

	// For dialog opens, use the trigger_id from the interaction.
	triggerID := callback.TriggerID

	// For view submissions, the channel comes from private_metadata.
	channelID := callback.Channel.ID
	if channelID == "" {
		channelID = event.SpaceID
	}

	a.sendResponse(ctx, channelID, event.ThreadID, triggerID, resp)
}

// --- Response sending ---

func (a *Adapter) sendResponse(ctx context.Context, channelID, threadTS, triggerID string, resp *chatapp.EventResponse) {
	if resp == nil {
		return
	}

	if resp.Dialog != nil && triggerID != "" {
		if err := a.OpenDialog(ctx, triggerID, *resp.Dialog); err != nil {
			a.log.Error("failed to open dialog", "error", err)
		}
	}

	if resp.Message != nil {
		msg := *resp.Message
		if msg.SpaceID == "" {
			msg.SpaceID = channelID
		}
		if msg.ThreadID == "" {
			msg.ThreadID = threadTS
		}
		if _, err := a.SendMessage(ctx, msg); err != nil {
			a.log.Error("failed to send response message", "error", err)
		}
	}
}

func (a *Adapter) sendEphemeral(ctx context.Context, channelID, userID string, resp *chatapp.EventResponse) {
	if resp == nil || resp.Message == nil {
		return
	}

	opts := []slackapi.MsgOption{
		slackapi.MsgOptionText(resp.Message.Text, false),
	}
	if resp.Message.Card != nil {
		blocks := renderBlocks(resp.Message.Card)
		opts = append(opts, slackapi.MsgOptionBlocks(blocks...))
	}

	if _, err := a.client.PostEphemeralContext(ctx, channelID, userID, opts...); err != nil {
		a.log.Error("failed to send ephemeral message", "channel", channelID, "user", userID, "error", err)
	}
}
