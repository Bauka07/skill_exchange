package usecase

import (
	"context"

	"github.com/QosmuratSamat0/pairexx/notification-service/internal/domain"
)

type usecase struct {
	channels []domain.Channel
}

func New(channels ...domain.Channel) domain.Usecase {
	return &usecase{channels: channels}
}

func (u *usecase) Notify(ctx context.Context, n domain.Notification) error {
	// Best-effort fanout; stop on first error for now (simple MVP).
	for _, ch := range u.channels {
		if ch == nil {
			continue
		}
		if err := ch.Send(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

