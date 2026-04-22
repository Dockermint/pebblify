// Package health exposes the opt-in HTTP health probe server and its
// underlying state machine.
//
// The probe state tracks three signals (started, ready, alive) updated by
// the migration driver; the HTTP server serves them on /healthz/startup,
// /healthz/ready, and /healthz/live in a Kubernetes-compatible shape. A
// ticker keeps the liveness signal fresh during long-running conversions
// so operators can distinguish a wedged process from a healthy one.
package health
