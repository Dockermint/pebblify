package notify

import "context"

// NoopNotifier is the Notifier used when notify.enable = false. It discards
// every event and always returns nil.
type NoopNotifier struct{}

// Notify implements Notifier. The call is a no-op.
func (NoopNotifier) Notify(_ context.Context, _ Event) error {
	return nil
}
