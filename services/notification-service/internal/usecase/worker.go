package usecase

import (
	"context"
	"encoding/json"
	"github.com/QosmuratSamat0/pairexx/pkg/mq"
	"github.com/rs/zerolog/log"
	"time"
)

type NotificationWorker struct {
	mq *mq.JetStream
}

func NewNotificationWorker(mq *mq.JetStream) *NotificationWorker {
	return &NotificationWorker{mq: mq}
}

type matchFoundEvent struct {
	ID        string    `json:"id"`
	UserA     string    `json:"user_a"`
	UserB     string    `json:"user_b"`
	Mode      string    `json:"mode"`
	CreatedAt time.Time `json:"created_at"`
}

func (w *NotificationWorker) Start(ctx context.Context) error {
	log.Info().Msg("Starting Notification Worker (NATS Subscriber)")

	return w.mq.Subscribe(ctx, "EVENTS", "match.found", "notification-service-matcher", func(data []byte) error {
		var event struct {
			ID    string `json:"id"`
			UserA string `json:"user_a"`
			UserB string `json:"user_b"`
			Mode  string `json:"mode"`
		}

		if err := json.Unmarshal(data, &event); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal match.found event")
			return nil // Return nil to avoid retry on bad JSON
		}

		log.Info().
			Str("room_id", event.ID).
			Str("user_a", event.UserA).
			Str("user_b", event.UserB).
			Msg("Processing match.found notification")

		// 1. Send push/email/etc. (Simulated)
		w.sendNotification(event.UserA, "Match found! Room: "+event.ID)
		w.sendNotification(event.UserB, "Match found! Room: "+event.ID)

		return nil
	})
}

func (w *NotificationWorker) sendNotification(userID, message string) {
	// Real implementation would call a push service or email service
	log.Debug().Str("user_id", userID).Str("msg", message).Msg("Notification SENT")
}
