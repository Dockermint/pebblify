package notify

import "context"

// noopNotifier is the Notifier used when notify.enable = false. It discards
// every event and always returns nil. It is unexported; callers obtain an
// instance via New which returns it through the Notifier interface.
type noopNotifier struct{}

// Notify implements Notifier. The call is a no-op.
func (noopNotifier) Notify(_ context.Context, _ Event) error {
	return nil
}
