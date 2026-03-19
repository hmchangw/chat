package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/caarlos0/env/v11"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type config struct {
	NatsURL  string `env:"NATS_URL"  envDefault:"nats://localhost:4222"`
	SiteID   string `env:"SITE_ID"   envDefault:"default"`
	MongoURI string `env:"MONGO_URI" envDefault:"mongodb://localhost:27017"`
	MongoDB  string `env:"MONGO_DB"  envDefault:"chat"`
}

// mongoMemberLookup implements MemberLookup using MongoDB.
type mongoMemberLookup struct {
	col *mongo.Collection
}

func (m *mongoMemberLookup) ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error) {
	filter := map[string]string{"roomId": roomID}
	cursor, err := m.col.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var subs []model.Subscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, err
	}
	return subs, nil
}

func main() {
	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}
	subCol := mongoClient.Database(cfg.MongoDB).Collection("subscriptions")
	memberLookup := &mongoMemberLookup{col: subCol}

	nc, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		slog.Error("jetstream init failed", "error", err)
		os.Exit(1)
	}

	fanoutCfg := stream.Fanout(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     fanoutCfg.Name,
		Subjects: fanoutCfg.Subjects,
	}); err != nil {
		slog.Error("create fanout stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, fanoutCfg.Name, jetstream.ConsumerConfig{
		Durable:   "notification-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		slog.Error("create consumer failed", "error", err)
		os.Exit(1)
	}

	publisher := &natsPublisher{nc: nc}
	handler := NewHandler(memberLookup, publisher)

	cctx, err := cons.Consume(func(msg jetstream.Msg) {
		if err := handler.HandleMessage(ctx, msg.Data()); err != nil {
			slog.Error("handle message failed", "error", err)
			msg.Nak()
			return
		}
		msg.Ack()
	})
	if err != nil {
		slog.Error("consume failed", "error", err)
		os.Exit(1)
	}

	slog.Info("notification-worker started", "site", cfg.SiteID)

	shutdown.Wait(ctx,
		func(ctx context.Context) error { cctx.Stop(); return nil },
		func(ctx context.Context) error { nc.Drain(); return nil },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	)
}

// natsPublisher adapts *nats.Conn to the Publisher interface.
type natsPublisher struct {
	nc *nats.Conn
}

func (p *natsPublisher) Publish(subject string, data []byte) error {
	return p.nc.Publish(subject, data)
}
