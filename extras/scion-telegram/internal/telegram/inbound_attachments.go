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

package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	scionapi "github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/projectfs"
)

// Inbound attachments. Before the plugin downloads a file a sender
// attached, it asks the hub whether the sender may send attachments to the
// addressed agents and how the hub takes them:
//
//   - "artifact": the file is downloaded into memory and uploaded to the
//     hub once per recipient send; each send names its own upload ids.
//   - "legacy": the file is staged once in the project scratchpad
//     (legacyStage) and every send names its path. If no recipient accepts
//     the message, the staged entry is removed.
//   - anything else: the text is delivered without the file and the sender
//     is told so.
//
// A hub without the check route (a plain 404) is an older hub: the plugin
// stages as in "legacy" mode, after its own sender checks.

const (
	inboundAttachmentCheckPath  = "/api/v1/broker/inbound/attachments/check"
	inboundAttachmentUploadPath = "/api/v1/broker/inbound/attachments"

	attachmentUploadsArtifact    = "artifact"
	attachmentUploadsLegacy      = "legacy"
	attachmentUploadsUnavailable = "unavailable"

	// attachmentSurface and attachmentAudience describe where an inbound
	// attachment came from: Telegram messages reach agents only from groups.
	attachmentSurface  = "telegram"
	attachmentAudience = "group"

	// hubAnswerMaxBytes bounds how much of a hub answer the plugin reads.
	hubAnswerMaxBytes = 1 << 20

	attachmentsWithheldNotice      = "Attachments can't be delivered to agents on this hub right now; your message was sent without them."
	attachmentsWithheldNothingSent = "Attachments can't be delivered to agents on this hub right now, so nothing was sent."
	attachmentsRefusedText         = "You can't send attachments to the addressed agents in this project, so your message was not delivered."
)

// errAttachmentCheckAbsent reports that the hub does not serve the
// attachment check route (an older hub).
var errAttachmentCheckAbsent = errors.New("hub does not serve the attachment check")

// errAttachmentTooLarge reports a file over the applicable size limit.
var errAttachmentTooLarge = errors.New("attachment too large")

// attachmentCheckRequest is the body of the hub's attachment check.
type attachmentCheckRequest struct {
	Sender    string   `json:"sender"`
	ProjectID string   `json:"project_id"`
	Targets   []string `json:"targets,omitempty"`
	Surface   string   `json:"surface"`
}

// attachmentCheckAnswer is the hub's answer to the attachment check.
type attachmentCheckAnswer struct {
	Authorized       bool   `json:"authorized"`
	Uploads          string `json:"uploads"`
	DeliveryMaxBytes int64  `json:"delivery_max_bytes,omitempty"`
}

// uploadedAttachment and attachmentUploadFailure mirror the hub's upload
// answer (pkg/hubclient AttachmentUploadResult).
type uploadedAttachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

type attachmentUploadFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

type attachmentUploadResult struct {
	Attachments []uploadedAttachment      `json:"attachments"`
	Failures    []attachmentUploadFailure `json:"failures"`
}

// isHubErrorEnvelope reports whether body is the hub's JSON error
// envelope, {"error":{"code":...}}.
func isHubErrorEnvelope(body []byte) bool {
	var env struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return false
	}
	return env.Error != nil && env.Error.Code != ""
}

// hubErrorCode returns the code of a hub JSON error envelope, or "".
func hubErrorCode(body []byte) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	return env.Error.Code
}

// postToHub sends a broker-signed POST to the hub and returns the status
// and at most hubAnswerMaxBytes of the answer.
func (b *TelegramBrokerV2) postToHub(ctx context.Context, path, contentType string, body []byte) (int, []byte, error) {
	b.mu.RLock()
	hubURL := b.hubURL
	hmacKey := b.hmacKey
	brokerID := b.brokerID
	pluginName := b.pluginName
	b.mu.RUnlock()

	if hubURL == "" {
		return 0, nil, errors.New("no hub URL configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hubURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Scion-Plugin-Name", pluginName)
	if brokerID != "" && hmacKey != "" {
		if err := signInboundRequest(req, brokerID, hmacKey); err != nil {
			return 0, nil, fmt.Errorf("sign request: %w", err)
		}
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, hubAnswerMaxBytes))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read answer: %w", err)
	}
	return resp.StatusCode, answer, nil
}

// checkInboundAttachments asks the hub whether sender may send attachments
// to targets in projectID, and how the hub takes them. It returns
// errAttachmentCheckAbsent only for a 404 that is not the hub's JSON error
// envelope; every other non-200 answer is an error.
func (b *TelegramBrokerV2) checkInboundAttachments(ctx context.Context, sender, projectID string, targets []string) (attachmentCheckAnswer, error) {
	body, err := json.Marshal(attachmentCheckRequest{
		Sender:    sender,
		ProjectID: projectID,
		Targets:   targets,
		Surface:   attachmentSurface,
	})
	if err != nil {
		return attachmentCheckAnswer{}, err
	}
	status, answer, err := b.postToHub(ctx, inboundAttachmentCheckPath, "application/json", body)
	if err != nil {
		return attachmentCheckAnswer{}, fmt.Errorf("attachment check: %w", err)
	}
	switch {
	case status == http.StatusOK:
		var out attachmentCheckAnswer
		if err := json.Unmarshal(answer, &out); err != nil {
			return attachmentCheckAnswer{}, fmt.Errorf("attachment check: decode answer: %w", err)
		}
		return out, nil
	case status == http.StatusNotFound && !isHubErrorEnvelope(answer):
		return attachmentCheckAnswer{}, errAttachmentCheckAbsent
	default:
		return attachmentCheckAnswer{}, fmt.Errorf("attachment check: HTTP %d %s", status, hubErrorCode(answer))
	}
}

// uploadInboundAttachment uploads one file to the hub as an upload of
// sender, for a send to targets. The metadata fields precede the file part.
func (b *TelegramBrokerV2) uploadInboundAttachment(ctx context.Context, sender, projectID string, targets []string, name string, data []byte) (attachmentUploadResult, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fields := [][2]string{
		{"sender", sender},
		{"project_id", projectID},
		{"surface", attachmentSurface},
		{"audience", attachmentAudience},
	}
	for _, t := range targets {
		fields = append(fields, [2]string{"targets", t})
	}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return attachmentUploadResult{}, err
		}
	}
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		return attachmentUploadResult{}, err
	}
	if _, err := fw.Write(data); err != nil {
		return attachmentUploadResult{}, err
	}
	if err := mw.Close(); err != nil {
		return attachmentUploadResult{}, err
	}

	status, answer, err := b.postToHub(ctx, inboundAttachmentUploadPath, mw.FormDataContentType(), buf.Bytes())
	if err != nil {
		return attachmentUploadResult{}, fmt.Errorf("attachment upload: %w", err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return attachmentUploadResult{}, fmt.Errorf("attachment upload: HTTP %d %s", status, hubErrorCode(answer))
	}
	var out attachmentUploadResult
	if err := json.Unmarshal(answer, &out); err != nil {
		return attachmentUploadResult{}, fmt.Errorf("attachment upload: decode answer: %w", err)
	}
	return out, nil
}

// telegramAttachment is the downloadable file of a Telegram message.
type telegramAttachment struct {
	FileID string
	Name   string // sanitized: a single path component
	Kind   string // "Document", "Photo", "Audio" or "Video"
	Size   int64  // as reported by Telegram; 0 when unknown
}

// placeholder is the line naming the attachment in the delivered text.
func (a telegramAttachment) placeholder() string {
	return fmt.Sprintf("📎 [%s attached: %s]", a.Kind, a.Name)
}

// telegramAttachmentOf returns the downloadable file of tgMsg, if any.
func telegramAttachmentOf(tgMsg *TGMessage) (telegramAttachment, bool) {
	var a telegramAttachment
	switch {
	case tgMsg == nil:
		return a, false
	case tgMsg.Document != nil:
		a.FileID = tgMsg.Document.FileID
		a.Size = tgMsg.Document.FileSize
		a.Name = tgMsg.Document.FileName
		if a.Name == "" {
			a.Name = tgMsg.Document.FileUniqueID
		}
		a.Kind = "Document"
	case len(tgMsg.Photo) > 0:
		largest := tgMsg.Photo[len(tgMsg.Photo)-1]
		a.FileID = largest.FileID
		a.Size = largest.FileSize
		a.Name = fmt.Sprintf("photo_%s.jpg", largest.FileUniqueID)
		a.Kind = "Photo"
	case tgMsg.Audio != nil:
		a.FileID = tgMsg.Audio.FileID
		a.Size = tgMsg.Audio.FileSize
		a.Name = tgMsg.Audio.FileName
		if a.Name == "" {
			// Build a descriptive name from the title if available.
			if tgMsg.Audio.Title != "" {
				a.Name = tgMsg.Audio.Title + ".ogg"
			} else {
				a.Name = fmt.Sprintf("audio_%s.ogg", tgMsg.Audio.FileUniqueID)
			}
		}
		a.Kind = "Audio"
	case tgMsg.Video != nil:
		a.FileID = tgMsg.Video.FileID
		a.Size = tgMsg.Video.FileSize
		a.Name = tgMsg.Video.FileName
		if a.Name == "" {
			a.Name = fmt.Sprintf("video_%s.mp4", tgMsg.Video.FileUniqueID)
		}
		a.Kind = "Video"
	default:
		return a, false
	}
	// Reduce user-controlled names (Document.FileName, Audio.Title) to a
	// single component. Backslashes become slashes first so both Windows
	// and Unix separators are stripped whatever the host OS.
	a.Name = filepath.Base(strings.ReplaceAll(a.Name, "\\", "/"))
	if a.Name == "." || a.Name == "/" || a.Name == "" {
		a.Name = fmt.Sprintf("file_%s", a.FileID)
	}
	return a, true
}

// boundedReadCloser fails with errAttachmentTooLarge once more than limit
// bytes have been read.
type boundedReadCloser struct {
	r     io.Reader
	c     io.Closer
	limit int64
	read  int64
}

func newBoundedReadCloser(rc io.ReadCloser, limit int64) *boundedReadCloser {
	return &boundedReadCloser{r: io.LimitReader(rc, limit+1), c: rc, limit: limit}
}

func (b *boundedReadCloser) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return 0, fmt.Errorf("%w (max %d bytes)", errAttachmentTooLarge, b.limit)
	}
	return n, err
}

func (b *boundedReadCloser) Close() error { return b.c.Close() }

// downloadTelegramFile downloads att from Telegram. The returned reader
// fails once more than limit bytes are read; a file Telegram reports as
// larger than limit is refused before it is requested.
func downloadTelegramFile(ctx context.Context, api *TelegramAPIClient, att telegramAttachment, limit int64) (io.ReadCloser, error) {
	if att.Size > limit {
		return nil, fmt.Errorf("%w (%d bytes, max %d)", errAttachmentTooLarge, att.Size, limit)
	}
	tgFile, err := api.GetFile(ctx, att.FileID)
	if err != nil {
		return nil, fmt.Errorf("getFile: %w", err)
	}
	if tgFile.FileSize > limit {
		return nil, fmt.Errorf("%w (%d bytes, max %d)", errAttachmentTooLarge, tgFile.FileSize, limit)
	}
	body, err := api.DownloadFile(ctx, tgFile.FilePath)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	return newBoundedReadCloser(body, limit), nil
}

// onLegacyStage is called on every legacyStage call. Tests replace it to
// observe which delivery modes stage.
var onLegacyStage = func() {}

// legacyStage writes one inbound attachment into the project scratchpad,
// in the per-message entry .attachments/_telegram/<entry>/<name>, and
// returns the agent-visible path and the scratchpad it wrote to. It is the
// plugin's only scratchpad writer; it runs only after the hub authorized
// the sender (or, on an older hub, after the plugin's own sender checks).
func legacyStage(projectSlug, projectID, entry, name string, body io.Reader) (string, projectfs.Resolved, error) {
	onLegacyStage()
	if projectID == "" {
		return "", projectfs.Resolved{}, fmt.Errorf("no project for %q: attachments are stored in the project's scratchpad", name)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", projectfs.Resolved{}, fmt.Errorf("resolve home directory: %w", err)
	}
	// The scratchpad resolves through the layout authority (local or nfs).
	// Never fall back to a local directory for an nfs-backed scratchpad: no
	// agent mounts it.
	scratchpad, err := resolveSharedDir(home, projectSlug, projectID, projectfs.ScratchpadName)
	if err != nil {
		return "", projectfs.Resolved{}, fmt.Errorf("resolve scratchpad shared dir for %q: %w", name, err)
	}
	agentPath, err := projectfs.WriteAttachment(scratchpad.Resolved, true,
		projectfs.SharedDirMount(scionapi.SharedDir{Name: projectfs.ScratchpadName}, ""),
		projectfs.AttachmentChannelTelegram, entry, name, body)
	if err != nil {
		return "", projectfs.Resolved{}, fmt.Errorf("write file: %w", err)
	}
	return agentPath, scratchpad.Resolved, nil
}

// inboundAttachment is an attachment being delivered with one Telegram
// message to every recipient send.
type inboundAttachment struct {
	att telegramAttachment

	// Artifact mode: the downloaded bytes, uploaded once per send.
	data []byte

	// Legacy mode: the staged entry and its agent-visible path.
	legacy     bool
	agentPath  string
	scratchpad projectfs.Resolved
	entry      string
	accepted   bool // some recipient accepted a send naming agentPath
}

// inboundAttachmentPlan is the outcome of preparing a message's attachment.
type inboundAttachmentPlan struct {
	// attachment is set when the file is delivered with the message.
	attachment *inboundAttachment
	// refused is set when the message must not be delivered at all.
	refused bool
}

// prepareInboundAttachment decides how the attachment of tgMsg (if any) is
// delivered to recipients, before anything is downloaded or written, and
// downloads or stages it accordingly. It replies to the sender in chat when
// the attachment is withheld or the message is refused. hasText reports
// whether the message has text to deliver without the attachment.
func (b *TelegramBrokerV2) prepareInboundAttachment(ctx context.Context, api *TelegramAPIClient, tgMsg *TGMessage, link *GroupLink, sender string, recipients []string, hasText bool) inboundAttachmentPlan {
	att, ok := telegramAttachmentOf(tgMsg)
	if !ok {
		return inboundAttachmentPlan{}
	}
	reply := func(text string) { replyToSender(ctx, api, tgMsg, text) }
	withheld := func() inboundAttachmentPlan {
		if hasText {
			reply(attachmentsWithheldNotice)
		} else {
			reply(attachmentsWithheldNothingSent)
		}
		return inboundAttachmentPlan{}
	}

	answer, err := b.checkInboundAttachments(ctx, sender, link.ProjectID, recipients)
	switch {
	case errors.Is(err, errAttachmentCheckAbsent):
		// An older hub: stage after the plugin's own sender checks, which
		// passed before this point.
		b.log.Info("Hub has no attachment check; staging the attachment in the project scratchpad")
		return b.stageInboundAttachment(ctx, api, link, att, reply)
	case err != nil:
		b.log.Warn("Attachment check failed; delivering the message without its attachment", "error", err)
		return withheld()
	case !answer.Authorized:
		b.log.Info("Hub refused the attachment sender", "chat_id", tgMsg.Chat.ID, "recipients", len(recipients))
		reply(attachmentsRefusedText)
		return inboundAttachmentPlan{refused: true}
	}

	switch answer.Uploads {
	case attachmentUploadsArtifact:
		limit := answer.DeliveryMaxBytes
		if limit <= 0 {
			b.log.Warn("Attachment check answered artifact without a size limit; delivering the message without its attachment")
			return withheld()
		}
		limit = min(limit, maxTelegramFileSize)
		data, err := b.downloadAttachmentBytes(ctx, api, att, limit)
		if errors.Is(err, errAttachmentTooLarge) {
			b.log.Info("Inbound attachment over the hub's size limit", "size", att.Size, "limit", limit)
			reply(attachmentTooLargeText(att.Name, limit, hasText))
			return inboundAttachmentPlan{}
		}
		if err != nil {
			b.log.Error("Failed to download telegram file", "error", err)
			reply(attachmentFailureText(att.Name, err))
			return inboundAttachmentPlan{}
		}
		return inboundAttachmentPlan{attachment: &inboundAttachment{att: att, data: data}}
	case attachmentUploadsLegacy:
		return b.stageInboundAttachment(ctx, api, link, att, reply)
	default:
		return withheld()
	}
}

// downloadAttachmentBytes downloads att into memory, at most limit bytes.
func (b *TelegramBrokerV2) downloadAttachmentBytes(ctx context.Context, api *TelegramAPIClient, att telegramAttachment, limit int64) ([]byte, error) {
	body, err := downloadTelegramFile(ctx, api, att, limit)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

// stageInboundAttachment downloads att and stages it in a per-message
// scratchpad entry for every recipient send to name.
func (b *TelegramBrokerV2) stageInboundAttachment(ctx context.Context, api *TelegramAPIClient, link *GroupLink, att telegramAttachment, reply func(string)) inboundAttachmentPlan {
	fail := func(err error) inboundAttachmentPlan {
		b.log.Error("Failed to stage telegram file", "error", err)
		// The error can name host paths; the sender only gets fixed text.
		reply(attachmentFailureText(att.Name, err))
		return inboundAttachmentPlan{}
	}
	body, err := downloadTelegramFile(ctx, api, att, maxTelegramFileSize)
	if err != nil {
		return fail(err)
	}
	defer body.Close()
	entry := fmt.Sprintf("tg_%d", time.Now().UnixNano())
	agentPath, scratchpad, err := legacyStage(link.ProjectSlug, link.ProjectID, entry, att.Name, body)
	if err != nil {
		return fail(err)
	}
	b.log.Info("Staged telegram file", "type", att.Kind, "agent_path", agentPath)
	return inboundAttachmentPlan{attachment: &inboundAttachment{
		att: att, legacy: true, agentPath: agentPath, scratchpad: scratchpad, entry: entry,
	}}
}

// replyToSender sends text to the chat (and forum topic) of tgMsg.
func replyToSender(ctx context.Context, api *TelegramAPIClient, tgMsg *TGMessage, text string) {
	api.SendMessage(ctx, tgMsg.Chat.ID, text, "", SendOption{MessageThreadID: tgMsg.MessageThreadID}) //nolint:errcheck
}

// attachmentTooLargeText tells the sender an attachment was over the
// hub's limit.
func attachmentTooLargeText(name string, limit int64, hasText bool) string {
	if hasText {
		return fmt.Sprintf("Attachment %q is larger than agents on this hub accept (max %d bytes); your message was sent without it.", name, limit)
	}
	return fmt.Sprintf("Attachment %q is larger than agents on this hub accept (max %d bytes), so nothing was sent.", name, limit)
}

// sendInbound delivers msg for one recipient agent, carrying the message's
// attachment if it has one: in artifact mode it uploads the file for this
// send and names the upload, in legacy mode it names the staged path. It
// reports the hub's status and error text; sent is false when nothing was
// sent because the upload failed.
func (b *TelegramBrokerV2) sendInbound(ctx context.Context, api *TelegramAPIClient, tgMsg *TGMessage, projectID, agentSlug, topic string, msg *messages.StructuredMessage, a *inboundAttachment) (status int, errMsg string, sent bool) {
	if a == nil {
		status, errMsg = b.deliverInboundWithAttachments(ctx, topic, msg, nil)
		return status, errMsg, true
	}
	if a.legacy {
		msg.Attachments = []string{a.agentPath}
		status, errMsg = b.deliverInboundWithAttachments(ctx, topic, msg, nil)
		if status >= 200 && status < 300 {
			a.accepted = true
		}
		return status, errMsg, true
	}

	ids, failure := b.uploadForSend(ctx, msg.Sender, projectID, agentSlug, a)
	if failure != "" {
		replyToSender(ctx, api, tgMsg, fmt.Sprintf("Attachment %q could not be uploaded for %s: %s. Your message was not sent to that agent.", a.att.Name, agentSlug, failure))
		return 0, "", false
	}
	status, errMsg = b.deliverInboundWithAttachments(ctx, topic, msg, ids)
	return status, errMsg, true
}

// uploadForSend uploads a's bytes for one send to agentSlug and returns
// the upload ids, or a failure text for the sender.
func (b *TelegramBrokerV2) uploadForSend(ctx context.Context, sender, projectID, agentSlug string, a *inboundAttachment) ([]string, string) {
	res, err := b.uploadInboundAttachment(ctx, sender, projectID, []string{agentSlug}, a.att.Name, a.data)
	if err != nil {
		b.log.Warn("Attachment upload failed", "agent", agentSlug, "error", err)
		return nil, "the hub did not accept the upload"
	}
	if len(res.Failures) > 0 {
		b.log.Warn("Attachment upload refused", "agent", agentSlug, "failures", len(res.Failures))
		return nil, res.Failures[0].Error
	}
	if len(res.Attachments) == 0 {
		b.log.Warn("Attachment upload stored nothing", "agent", agentSlug)
		return nil, "the hub did not store the file"
	}
	ids := make([]string, 0, len(res.Attachments))
	for _, u := range res.Attachments {
		ids = append(ids, u.ID)
	}
	return ids, ""
}

// finishInboundAttachment removes a staged legacy entry that no recipient
// accepted. A removal failure is logged with counts only.
func (b *TelegramBrokerV2) finishInboundAttachment(a *inboundAttachment) {
	if a == nil || !a.legacy || a.accepted {
		return
	}
	removed, err := projectfs.RemoveAttachmentEntry(a.scratchpad, projectfs.AttachmentChannelTelegram, a.entry)
	if err != nil {
		b.log.Warn("Failed to remove a staged attachment entry no recipient accepted", "entries", 1)
		return
	}
	b.log.Info("Removed a staged attachment entry no recipient accepted", "removed", removed)
}
