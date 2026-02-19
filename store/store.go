// Package store defines the persistence abstraction for sticky-backend.
// The default implementation is SQLite; Postgres may be added in the future.
// All write-path methods receive a Target so that future implementations can
// perform per-subscription lookups or side effects without API changes.
package store

import (
	"context"
	"time"

	"github.com/whisper-darkly/sticky-backend/config"
)

// ---- subscription states ----

// State is the persisted lifecycle state of a subscription.
type State string

const (
	// StateActive means the subscription is live; a recorder should be running.
	StateActive State = "active"

	// StateInactive is a soft-delete.  Inactive subscriptions are hidden from
	// all listings; re-adding the same driver+source reactivates the record.
	StateInactive State = "inactive"

	// StatePaused means the user has explicitly paused recording.
	// No recorder will be started until the subscription is resumed.
	StatePaused State = "paused"

	// StateError means the recorder has exceeded the error threshold within the
	// configured time window.  Not retried until explicitly reset.
	StateError State = "error"
)

// Subscription is the persisted record of a stream recording subscription.
// The natural key is (driver, source); each combination is unique.
type Subscription struct {
	ID           int64     `json:"id"`
	Driver       string    `json:"driver"`
	Source       string    `json:"source"`
	State        State     `json:"state"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Target bundles a Subscription with its effective configuration.
// Config is currently the global default; per-subscription overrides may be
// added to the store layer in the future without changing call sites.
type Target struct {
	Sub    *Subscription
	Config config.Data
}

// ---- worker events ----

// EventType classifies a worker lifecycle event.
type EventType string

const (
	// EventStarted is recorded when a recorder process is successfully launched.
	EventStarted EventType = "started"

	// EventExited is recorded when a recorder process terminates for any reason.
	// ExitCode is non-nil.  Exits that have a matching EventStopped for the
	// same PID are considered intentional and excluded from error counting.
	EventExited EventType = "exited"

	// EventStopped is recorded immediately before the backend sends SIGTERM to
	// a worker because of a user action (pause or restart).
	// Recording it before the signal allows the exit that follows to be
	// identified as intentional via PID correlation.
	// EventStopped also marks the boundary of an "intentional stop/start cycle":
	// the first EventStarted that arrives after the most recent EventStopped
	// defines the start of a new error-counting window.
	EventStopped EventType = "stopped"
)

// WorkerEvent is a single persisted lifecycle event for a worker process.
type WorkerEvent struct {
	ID             int64     `json:"id"`
	SubscriptionID int64     `json:"subscription_id"`
	PID            int       `json:"pid"`
	EventType      EventType `json:"event_type"`
	ExitCode       *int      `json:"exit_code,omitempty"` // non-nil only for EventExited
	TS             time.Time `json:"ts"`
}

// ---- store interface ----

// Store is the persistence abstraction.  All methods are context-aware.
type Store interface {
	// ---- subscriptions ----

	// CreateSubscription creates an active subscription for the given driver+source.
	// If one already exists in any state it is reactivated and returned.
	CreateSubscription(ctx context.Context, driver, source string) (*Subscription, error)

	// GetSubscription fetches a subscription by primary key.
	// Returns (nil, nil) when not found.
	GetSubscription(ctx context.Context, id int64) (*Subscription, error)

	// GetSubscriptionByKey fetches a subscription by driver+source.
	// Returns (nil, nil) when not found.
	GetSubscriptionByKey(ctx context.Context, driver, source string) (*Subscription, error)

	// ListVisible returns all subscriptions except inactive ones,
	// ordered by driver then source.
	ListVisible(ctx context.Context) ([]*Subscription, error)

	// ListActive returns only subscriptions in StateActive,
	// ordered by driver then source.
	ListActive(ctx context.Context) ([]*Subscription, error)

	// SetState transitions a subscription to the given state.
	// For StateError, errorMsg should describe the reason.
	SetState(ctx context.Context, id int64, state State, errorMsg string) error

	// ---- worker events ----

	// RecordWorkerEvent persists a single worker lifecycle event.
	// target carries the subscription and current config for future use by
	// richer implementations (e.g. a Postgres audit log).
	// exitCode must be non-nil for EventExited; it must be nil for all other types.
	RecordWorkerEvent(ctx context.Context, target Target, pid int, eventType EventType, exitCode *int) error

	// CycleResetAt returns the timestamp of the first EventStarted that arrived
	// after the most recent EventStopped for this subscription.  That moment is
	// defined as the start of the current "intentional stop/start cycle".
	//
	// Returns the zero time.Time if no such cycle has ever been recorded
	// (i.e. the subscription has never had a user-initiated stop followed by a
	// start).  In that case the caller should fall back to the error window.
	CycleResetAt(ctx context.Context, subscriptionID int64) (time.Time, error)

	// ErrorExitsSince counts EventExited records with a non-zero exit code whose
	// timestamp is after `since`, excluding exits whose PID has a corresponding
	// EventStopped record (intentional SIGTERM-induced exits).
	ErrorExitsSince(ctx context.Context, subscriptionID int64, since time.Time) (int, error)

	// RecentWorkerEvents returns up to limit worker events for a subscription,
	// ordered newest first.
	RecentWorkerEvents(ctx context.Context, subscriptionID int64, limit int) ([]WorkerEvent, error)

	// ---- lifecycle ----

	Close() error
}
