package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Telegram Bot API constants.
const (
	telegramAPIBase        = "https://api.telegram.org"
	telegramDefaultTimeout = 10 * time.Second
	telegramRetryBackoff   = 500 * time.Millisecond
	telegramBodyReadLimit  = 4096
)

// Errors returned by TelegramNotifier.
var (
	// ErrTelegramMissingToken indicates the constructor was given an empty token.
	ErrTelegramMissingToken = errors.New("telegram bot token is empty")
	// ErrTelegramMissingChannel indicates the constructor was given an empty channel id.
	ErrTelegramMissingChannel = errors.New("telegram channel id is empty")
	// ErrTelegramPermanent indicates the API returned a 4xx response; no retry was attempted.
	ErrTelegramPermanent = errors.New("telegram permanent failure")
	// ErrTelegramTransient indicates the API returned a 5xx response after the retry budget was spent.
	ErrTelegramTransient = errors.New("telegram transient failure")
)

// TelegramNotifier delivers Event messages to a Telegram channel via the HTTP
// Bot API. All network I/O uses the stdlib net/http client; no third-party
// library is imported. The bot token is never logged.
type TelegramNotifier struct {
	token     string
	channelID string
	client    *http.Client
	logger    *slog.Logger
}

// telegramRequest mirrors the subset of the sendMessage payload the daemon
// emits. parse_mode is fixed to HTML so renderMessage can use inline tags.
type telegramRequest struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

// NewTelegramNotifier constructs a Notifier that posts to the Bot API endpoint
// for token. If client is nil, a default client with a 10-second timeout is
// used.
//
// The constructor does not validate token or channelID emptiness at call time;
// config validation is responsible for that. Delivery attempts with empty
// credentials will fail with ErrTelegramMissingToken or
// ErrTelegramMissingChannel respectively.
func NewTelegramNotifier(token, channelID string, client *http.Client) *TelegramNotifier {
	if client == nil {
		client = &http.Client{Timeout: telegramDefaultTimeout}
	}
	return &TelegramNotifier{
		token:     token,
		channelID: channelID,
		client:    client,
		logger:    slog.Default(),
	}
}

// WithLogger returns a shallow copy of n wired to use l for structured logging.
// Useful for injecting a scoped logger; passing nil falls back to slog.Default().
func (n *TelegramNotifier) WithLogger(l *slog.Logger) *TelegramNotifier {
	cp := *n
	cp.logger = logger(l)
	return &cp
}

// Notify implements Notifier. It serializes event to a sendMessage payload,
// POSTs it, and applies the retry policy: one retry on 5xx after 500 ms,
// permanent failure on 4xx. Errors returned here are advisory; the worker
// logs them and continues the job pipeline.
func (n *TelegramNotifier) Notify(ctx context.Context, event Event) error {
	if n.token == "" {
		return ErrTelegramMissingToken
	}
	if n.channelID == "" {
		return ErrTelegramMissingChannel
	}

	body, err := json.Marshal(telegramRequest{
		ChatID:    n.channelID,
		Text:      renderMessage(event),
		ParseMode: "HTML",
	})
	if err != nil {
		return fmt.Errorf("marshal telegram request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, n.token)

	status, sendErr := n.send(ctx, endpoint, body)
	if sendErr == nil {
		return nil
	}

	// Retry once on transient (5xx) failures, not on 4xx or context errors.
	if !isTransient(status, sendErr) {
		n.logger.Error("telegram notify permanent failure",
			"channel_id", n.channelID, "status", status, "event", event.Kind.String())
		return sendErr
	}

	n.logger.Warn("telegram notify transient failure, retrying",
		"channel_id", n.channelID, "status", status, "event", event.Kind.String())

	if err := sleepCtx(ctx, telegramRetryBackoff); err != nil {
		return err
	}

	status, sendErr = n.send(ctx, endpoint, body)
	if sendErr == nil {
		return nil
	}
	n.logger.Error("telegram notify retry failed",
		"channel_id", n.channelID, "status", status, "event", event.Kind.String())
	return sendErr
}

// send performs a single POST attempt and classifies the response. It returns
// the HTTP status (0 on transport error) and an error typed as
// ErrTelegramPermanent for 4xx or ErrTelegramTransient for 5xx, or the
// transport error for non-HTTP failures.
func (n *TelegramNotifier) send(ctx context.Context, endpoint string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := n.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("post telegram request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Drain a bounded slice of the body so keep-alive connections can be reused.
	_, _ = io.CopyN(io.Discard, resp.Body, telegramBodyReadLimit)

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp.StatusCode, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return resp.StatusCode, fmt.Errorf("%w: http %d", ErrTelegramPermanent, resp.StatusCode)
	case resp.StatusCode >= 500:
		return resp.StatusCode, fmt.Errorf("%w: http %d", ErrTelegramTransient, resp.StatusCode)
	default:
		return resp.StatusCode, fmt.Errorf("telegram unexpected status: http %d", resp.StatusCode)
	}
}

// isTransient reports whether the given status+error pair is eligible for a
// retry. Transport errors are treated as non-retryable because they are
// already subject to the http.Client timeout; only explicit 5xx responses are
// retried per spec.
func isTransient(status int, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return status >= 500 && status < 600
}

// sleepCtx sleeps for d or returns ctx.Err() if the context is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
