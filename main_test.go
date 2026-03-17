package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
)

func startTestServer(t *testing.T) *natsserver.Server {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1 // random available port
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	s := natstest.RunServer(&opts)
	t.Cleanup(s.Shutdown)
	return s
}

func TestRouterDeliversMessageToRoomSubject(t *testing.T) {
	s := startTestServer(t)

	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stop, err := StartRouter(ctx, nc)
	if err != nil {
		t.Fatalf("Failed to start router: %v", err)
	}
	defer stop()

	// Subscribe to the target room subject before publishing.
	received := make(chan *nats.Msg, 1)
	sub, err := nc.Subscribe("chat.room.general", func(msg *nats.Msg) {
		received <- msg
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}
	defer sub.Unsubscribe()
	nc.Flush()

	// Publish a message to JetStream.
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("Failed to create JetStream context: %v", err)
	}

	msg := Message{
		RoomID:    "general",
		UserID:    "alice",
		Content:   "hello world",
		Timestamp: time.Now().UTC().Truncate(time.Second),
	}
	data, _ := json.Marshal(msg)

	_, err = js.Publish(ctx, "chat.messages", data)
	if err != nil {
		t.Fatalf("Failed to publish to JetStream: %v", err)
	}

	// Wait for the routed message.
	select {
	case routed := <-received:
		var got Message
		if err := json.Unmarshal(routed.Data, &got); err != nil {
			t.Fatalf("Failed to unmarshal routed message: %v", err)
		}
		if got.RoomID != "general" {
			t.Errorf("expected room_id=general, got %s", got.RoomID)
		}
		if got.UserID != "alice" {
			t.Errorf("expected user_id=alice, got %s", got.UserID)
		}
		if got.Content != "hello world" {
			t.Errorf("expected content=hello world, got %s", got.Content)
		}
	case <-ctx.Done():
		t.Fatal("Timed out waiting for routed message")
	}
}

func TestRouterRoutesToCorrectRoom(t *testing.T) {
	s := startTestServer(t)

	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stop, err := StartRouter(ctx, nc)
	if err != nil {
		t.Fatalf("Failed to start router: %v", err)
	}
	defer stop()

	// Subscribe to two different rooms.
	room1 := make(chan *nats.Msg, 1)
	room2 := make(chan *nats.Msg, 1)

	sub1, _ := nc.Subscribe("chat.room.room1", func(msg *nats.Msg) { room1 <- msg })
	sub2, _ := nc.Subscribe("chat.room.room2", func(msg *nats.Msg) { room2 <- msg })
	defer sub1.Unsubscribe()
	defer sub2.Unsubscribe()
	nc.Flush()

	js, _ := jetstream.New(nc)

	// Send a message to room2 only.
	msg := Message{RoomID: "room2", UserID: "bob", Content: "hi room2"}
	data, _ := json.Marshal(msg)
	_, err = js.Publish(ctx, "chat.messages", data)
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// room2 should receive it.
	select {
	case routed := <-room2:
		var got Message
		json.Unmarshal(routed.Data, &got)
		if got.RoomID != "room2" {
			t.Errorf("expected room_id=room2, got %s", got.RoomID)
		}
	case <-ctx.Done():
		t.Fatal("Timed out waiting for message on room2")
	}

	// room1 should NOT receive it (short wait).
	select {
	case <-room1:
		t.Error("room1 should not have received a message destined for room2")
	case <-time.After(200 * time.Millisecond):
		// expected
	}
}

func TestRouterTerminatesInvalidJSON(t *testing.T) {
	s := startTestServer(t)

	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stop, err := StartRouter(ctx, nc)
	if err != nil {
		t.Fatalf("Failed to start router: %v", err)
	}
	defer stop()

	// Subscribe to a wildcard to catch any routed message.
	routed := make(chan *nats.Msg, 1)
	sub, _ := nc.Subscribe("chat.room.*", func(msg *nats.Msg) { routed <- msg })
	defer sub.Unsubscribe()
	nc.Flush()

	js, _ := jetstream.New(nc)

	// Publish invalid JSON, then a valid message.
	_, err = js.Publish(ctx, "chat.messages", []byte("not json"))
	if err != nil {
		t.Fatalf("Failed to publish invalid message: %v", err)
	}

	valid := Message{RoomID: "lobby", UserID: "carol", Content: "after bad msg"}
	data, _ := json.Marshal(valid)
	_, err = js.Publish(ctx, "chat.messages", data)
	if err != nil {
		t.Fatalf("Failed to publish valid message: %v", err)
	}

	// The valid message should still come through (bad one was Term'd).
	select {
	case msg := <-routed:
		var got Message
		json.Unmarshal(msg.Data, &got)
		if got.RoomID != "lobby" {
			t.Errorf("expected room_id=lobby, got %s", got.RoomID)
		}
	case <-ctx.Done():
		t.Fatal("Timed out — router may have stalled on invalid message")
	}
}
