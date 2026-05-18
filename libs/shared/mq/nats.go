package mq

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type JetStream struct {
	nc *nats.Conn
	js jetstream.JetStream
}

func NewJetStream(url string) (*JetStream, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	return &JetStream{nc: nc, js: js}, nil
}

func (j *JetStream) Publish(ctx context.Context, subject string, data interface{}) error {
	if j == nil || j.js == nil {
		return errors.New("jetstream not initialized")
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = j.js.Publish(ctx, subject, b)
	return err
}

func (j *JetStream) Subscribe(ctx context.Context, stream, subject, consumerName string, handler func([]byte) error) error {
	if j == nil || j.js == nil {
		return errors.New("jetstream not initialized")
	}
	// Ensure stream exists
	_, _ = j.js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     stream,
		Subjects: []string{subject},
	})

	cons, err := j.js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable: consumerName,
	})
	if err != nil {
		return err
	}

	iter, _ := cons.Messages()
	go func() {
		for {
			msg, err := iter.Next()
			if err != nil {
				return
			}
			if err := handler(msg.Data()); err == nil {
				if err := msg.Ack(); err != nil {
					// In a real app, use a logger here
				}
			} else {
				if err := msg.Nak(); err != nil {
					// In a real app, use a logger here
				}
			}
		}
	}()
	return nil
}

func (j *JetStream) Close() {
	if j == nil || j.nc == nil {
		return
	}
	j.nc.Close()
}
