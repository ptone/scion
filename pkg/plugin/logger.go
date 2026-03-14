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

package plugin

import (
	"io"
	"log"
	"log/slog"

	hclog "github.com/hashicorp/go-hclog"
)

// hclogAdapter wraps slog.Logger to implement hashicorp/go-hclog.Logger.
// This allows go-plugin to use scion's existing slog-based logging.
type hclogAdapter struct {
	logger *slog.Logger
	name   string
	args   []interface{}
}

func newHclogAdapter(logger *slog.Logger) hclog.Logger {
	return &hclogAdapter{logger: logger}
}

func (h *hclogAdapter) Log(level hclog.Level, msg string, args ...interface{}) {
	allArgs := append(h.args, args...)
	switch level {
	case hclog.Trace, hclog.Debug:
		h.logger.Debug(msg, allArgs...)
	case hclog.Info:
		h.logger.Info(msg, allArgs...)
	case hclog.Warn:
		h.logger.Warn(msg, allArgs...)
	case hclog.Error:
		h.logger.Error(msg, allArgs...)
	}
}

func (h *hclogAdapter) Trace(msg string, args ...interface{}) {
	h.Log(hclog.Trace, msg, args...)
}

func (h *hclogAdapter) Debug(msg string, args ...interface{}) {
	h.Log(hclog.Debug, msg, args...)
}

func (h *hclogAdapter) Info(msg string, args ...interface{}) {
	h.Log(hclog.Info, msg, args...)
}

func (h *hclogAdapter) Warn(msg string, args ...interface{}) {
	h.Log(hclog.Warn, msg, args...)
}

func (h *hclogAdapter) Error(msg string, args ...interface{}) {
	h.Log(hclog.Error, msg, args...)
}

func (h *hclogAdapter) IsTrace() bool { return false }
func (h *hclogAdapter) IsDebug() bool { return true }
func (h *hclogAdapter) IsInfo() bool  { return true }
func (h *hclogAdapter) IsWarn() bool  { return true }
func (h *hclogAdapter) IsError() bool { return true }

func (h *hclogAdapter) ImpliedArgs() []interface{} {
	return h.args
}

func (h *hclogAdapter) With(args ...interface{}) hclog.Logger {
	return &hclogAdapter{
		logger: h.logger,
		name:   h.name,
		args:   append(h.args, args...),
	}
}

func (h *hclogAdapter) Name() string {
	return h.name
}

func (h *hclogAdapter) Named(name string) hclog.Logger {
	newName := name
	if h.name != "" {
		newName = h.name + "." + name
	}
	return &hclogAdapter{
		logger: h.logger.With("subsystem", newName),
		name:   newName,
		args:   h.args,
	}
}

func (h *hclogAdapter) ResetNamed(name string) hclog.Logger {
	return &hclogAdapter{
		logger: h.logger.With("subsystem", name),
		name:   name,
		args:   h.args,
	}
}

func (h *hclogAdapter) SetLevel(hclog.Level)                  {}
func (h *hclogAdapter) GetLevel() hclog.Level                 { return hclog.Debug }
func (h *hclogAdapter) StandardLogger(opts *hclog.StandardLoggerOptions) *log.Logger {
	return log.Default()
}
func (h *hclogAdapter) StandardWriter(opts *hclog.StandardLoggerOptions) io.Writer {
	return io.Discard
}
