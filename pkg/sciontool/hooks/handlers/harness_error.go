/*
Copyright 2026 The Scion Authors.
*/

package handlers

import (
	"strings"
	"unicode"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hooks"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
	"github.com/GoogleCloudPlatform/scion/pkg/util/logging"
)

// harnessErrorReasonMaxRunes caps the harness_error reason stored in
// detail.message and sent to the Hub.
const harnessErrorReasonMaxRunes = 300

// harnessErrorSourceModelAPI names the source of a turn-end error that a
// harness reported on its completion hook: the model API call failed.
const harnessErrorSourceModelAPI = "model API"

// isTurnEndError reports whether event is the parent agent's turn ending
// on an error the harness reported (agent-end with Data.Error set). Such a
// turn puts the agent in harness_error: the harness is alive at its prompt
// but its turn failed. A sub-agent's agent-end is not the parent's turn and
// does not count. model-end and tool-end errors do not count either: the
// harness may retry a model call, and a failed tool is normal work.
func isTurnEndError(event *hooks.Event) bool {
	return event.Name == hooks.EventAgentEnd &&
		strings.TrimSpace(event.Data.Error) != "" &&
		!isSubagentEvent(event)
}

// harnessErrorReason returns the harness_error reason for a turn-end error
// event, in the form "<source>: <text>".
func harnessErrorReason(event *hooks.Event) string {
	return formatHarnessErrorReason(harnessErrorSourceModelAPI, event.Data.Error)
}

// formatHarnessErrorReason renders a harness_error reason as
// "<source>: <text>". The text is untrusted harness output: it is stripped
// of terminal escapes, put on one line, passed through the free-text
// credential redaction log records use (logging.SanitizeText) and the
// whole reason is cut to harnessErrorReasonMaxRunes runes.
func formatHarnessErrorReason(source, text string) string {
	text = singleLine(util.StripANSI(text))
	text = singleLine(logging.SanitizeText(text))
	reason := source
	if text != "" {
		reason = source + ": " + text
	}
	return truncateRunes(reason, harnessErrorReasonMaxRunes)
}

// singleLine replaces every run of whitespace and control characters in s
// with one space and trims the ends.
func singleLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
}

// truncateRunes cuts s to at most maxRunes runes, ending in "…" when it
// was cut.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-1]) + "…"
}
