// Package store defines the persistence abstraction for sticky-dvr backend.
package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ---- posture ----

// Posture is the user-level intent for a subscription.
type Posture string

const (
	PostureActive   Posture = "active"
	PosturePaused   Posture = "paused"
	PostureArchived Posture = "archived"
)

// ---- event types ----

// EventType classifies a worker lifecycle event.
type EventType string

const (
	EventStarted EventType = "started"
	EventExited  EventType = "exited"
	EventStopped EventType = "stopped"
)

// ---- domain types ----

type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type UserUpdate struct {
	Username     *string
	PasswordHash *string
	Role         *string
}

type Session struct {
	ID           uuid.UUID `json:"id"`
	UserID       int64     `json:"user_id"`
	RefreshToken string    `json:"-"`
	ExpiresAt    time.Time `json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`
}

type Source struct {
	ID             int64     `json:"id"`
	Driver         string    `json:"driver"`
	Username       string    `json:"username"`
	OverseerTaskID string    `json:"overseer_task_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type Subscription struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	SourceID  int64     `json:"source_id"`
	Posture   Posture   `json:"posture"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type WorkerEvent struct {
	ID        int64     `json:"id"`
	SourceID  int64     `json:"source_id"`
	PID       int       `json:"pid"`
	EventType EventType `json:"event_type"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	TS        time.Time `json:"ts"`
}

// ---- store interface ----

// Store is the persistence abstraction. All methods are context-aware.
type Store interface {
	// ---- users ----
	CreateUser(ctx context.Context, username, passwordHash, role string) (*User, error)
	GetUser(ctx context.Context, id int64) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	ListUsers(ctx context.Context) ([]*User, error)
	UpdateUser(ctx context.Context, id int64, fields UserUpdate) (*User, error)
	DeleteUser(ctx context.Context, id int64) error

	// ---- sessions ----
	CreateSession(ctx context.Context, userID int64, refreshToken string, expiresAt time.Time) (*Session, error)
	GetSessionByRefreshToken(ctx context.Context, refreshToken string) (*Session, error)
	DeleteSession(ctx context.Context, id uuid.UUID) error
	DeleteExpiredSessions(ctx context.Context) error

	// ---- sources ----
	GetOrCreateSource(ctx context.Context, driver, username string) (*Source, error)
	GetSourceByKey(ctx context.Context, driver, username string) (*Source, error)
	ListSources(ctx context.Context) ([]*Source, error)
	SetSourceTaskID(ctx context.Context, sourceID int64, taskID string) error

	// ---- subscriptions ----
	CreateSubscription(ctx context.Context, userID, sourceID int64) (*Subscription, error)
	GetSubscription(ctx context.Context, userID, sourceID int64) (*Subscription, error)
	ListSubscriptionsByUser(ctx context.Context, userID int64) ([]*Subscription, error)
	ListActiveSubscriptions(ctx context.Context) ([]*Subscription, error)
	ListAllSubscriptions(ctx context.Context) ([]*Subscription, error)
	SetPosture(ctx context.Context, id int64, posture Posture) error
	GetSourceActiveSubscriberCount(ctx context.Context, sourceID int64) (int, error)

	// ---- worker events ----
	RecordWorkerEvent(ctx context.Context, sourceID int64, pid int, eventType EventType, exitCode *int) error
	RecentWorkerEvents(ctx context.Context, sourceID int64, limit int) ([]WorkerEvent, error)

	// ---- config ----
	GetConfig(ctx context.Context) (map[string]any, error)
	SetConfig(ctx context.Context, data map[string]any) error

	// ---- lifecycle ----
	Close() error
}
