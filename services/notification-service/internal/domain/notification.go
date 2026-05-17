package domain

import "context"

type Notification struct {
	Type    string         `json:"type"`
	UserID  string         `json:"user_id"`
	Payload map[string]any `json:"payload,omitempty"`
}

type Channel interface {
	Send(ctx context.Context, n Notification) error
}

type Usecase interface {
	Notify(ctx context.Context, n Notification) error
}

