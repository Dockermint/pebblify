// Package notify delivers daemon job lifecycle events to external sinks.
//
// Two implementations ship in v0.4.0: TelegramNotifier (HTTP Bot API, stdlib
// only) and an unexported noop used when notify.enable = false (discards every
// event). New backends plug in via the Notifier interface without touching the
// orchestrator.
package notify

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// EventKind identifies a job lifecycle transition.
type EventKind int

const (
	// EventStarted is emitted when the worker begins processing a job.
	EventStarted EventKind = iota
	// EventCompleted is emitted on successful job completion.
	EventCompleted
	// EventFailed is emitted when a job ends in error.
	EventFailed
)

// String returns a short human label for the event kind.
func (k EventKind) String() string {
	switch k {
	case EventStarted:
		return "started"
	case EventCompleted:
		return "completed"
	case EventFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Event is the payload handed to a Notifier. It contains everything a
// formatter needs to render a message; the raw Error is retained so structured
// sinks (e.g. future webhooks) can forward it verbatim.
type Event struct {
	// Kind is the lifecycle transition.
	Kind EventKind
	// JobID is the opaque job identifier.
	JobID string
	// JobURL is the snapshot URL being processed.
	JobURL string
	// Details is an optional free-form string appended to the message.
	Details string
	// Error is the underlying error for EventFailed events; nil otherwise.
	Error error
}

// Notifier is the contract satisfied by every notification backend.
type Notifier interface {
	// Notify delivers event. Non-fatal delivery errors (the notification
	// subsystem is advisory, not authoritative) are returned for the caller to
	// log; the worker does not abort the job on a Notify error.
	Notify(ctx context.Context, event Event) error
}

// ErrUnsupportedMode is returned by New when the NotifySection holds an
// unknown mode string. Config validation should reject this earlier.
var ErrUnsupportedMode = errors.New("unsupported notify mode")

// New constructs the appropriate Notifier implementation from the parsed
// configuration and the secrets bundle.
//
// When cfg.Enable is false the returned notifier is the unexported noop. When
// enabled with mode telegram a TelegramNotifier is built using the default
// HTTP client (10 s timeout). Other modes return ErrUnsupportedMode.
func New(cfg config.NotifySection, secrets config.Secrets) (Notifier, error) {
	if !cfg.Enable {
		return noopNotifier{}, nil
	}
	switch cfg.Mode {
	case config.NotifyModeTelegram:
		return NewTelegramNotifier(secrets.TelegramBotToken, cfg.ChannelID, nil), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedMode, cfg.Mode)
	}
}

// renderMessage produces the HTML-formatted text body used by the Telegram
// backend. It is package-private so the telegram_notifier test surface can be
// kept narrow; the format is identical across events.
//
// All fields that originate from untrusted input (JobURL, Details, Error text)
// are passed through html.EscapeString before concatenation so attacker-
// controlled payloads cannot inject Telegram markup or break the sendMessage
// call when parse_mode=HTML is used.
func renderMessage(event Event) string {
	header := fmt.Sprintf("<b>Pebblify job %s</b>", event.Kind)
	body := fmt.Sprintf("ID: <code>%s</code>\nURL: %s",
		event.JobID, html.EscapeString(event.JobURL))
	if event.Details != "" {
		body += "\n" + html.EscapeString(event.Details)
	}
	if event.Error != nil {
		body += "\nError: " + html.EscapeString(event.Error.Error())
	}
	return header + "\n" + body
}

// logger resolves a non-nil slog.Logger for use inside notifier
// implementations, falling back to the process default.
func logger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.Default()
}
