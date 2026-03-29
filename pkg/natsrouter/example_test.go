package natsrouter_test

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/natsrouter"
)

type GreetRequest struct {
	Message string `json:"message"`
}

type GreetResponse struct {
	Reply string `json:"reply"`
}

// Example_basicUsage demonstrates registering a handler with params.
func Example_basicUsage() {
	nc, _ := nats.Connect(nats.DefaultURL)
	router := natsrouter.New(nc, "my-service")

	// Register a handler — {userID} and {roomID} are extracted from the subject.
	// The pattern is automatically converted to a NATS wildcard for subscription.
	natsrouter.Register[GreetRequest, GreetResponse](
		router,
		"chat.user.{userID}.room.{roomID}.greet",
		func(ctx context.Context, p natsrouter.Params, req GreetRequest) (*GreetResponse, error) {
			userID := p.Get("userID")
			roomID := p.Get("roomID")
			reply := fmt.Sprintf("%s says %s in room %s", userID, req.Message, roomID)
			return &GreetResponse{Reply: reply}, nil
		},
	)
}

// Example_withMiddleware demonstrates using built-in middleware.
func Example_withMiddleware() {
	nc, _ := nats.Connect(nats.DefaultURL)
	router := natsrouter.New(nc, "my-service")

	// Recovery catches panics, Logging logs subject + duration.
	// Order matters: Recovery should be first to catch panics from all middleware.
	router.Use(natsrouter.Recovery())
	router.Use(natsrouter.Logging())

	natsrouter.Register[GreetRequest, GreetResponse](
		router,
		"chat.user.{userID}.greet",
		func(ctx context.Context, p natsrouter.Params, req GreetRequest) (*GreetResponse, error) {
			return &GreetResponse{Reply: "hello " + p.Get("userID")}, nil
		},
	)
}

type Room struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Example_noBodyHandler demonstrates RegisterNoBody for GET-style endpoints.
func Example_noBodyHandler() {
	nc, _ := nats.Connect(nats.DefaultURL)
	router := natsrouter.New(nc, "room-service")

	// No request body needed — the roomID comes from the subject.
	natsrouter.RegisterNoBody[Room](
		router,
		"chat.user.{userID}.request.rooms.get.{roomID}",
		func(ctx context.Context, p natsrouter.Params) (*Room, error) {
			roomID := p.Get("roomID")
			return &Room{ID: roomID, Name: "General"}, nil
		},
	)
}

// Example_errorHandling demonstrates user-facing vs internal errors.
func Example_errorHandling() {
	nc, _ := nats.Connect(nats.DefaultURL)
	router := natsrouter.New(nc, "room-service")

	natsrouter.Register(
		router,
		"chat.user.{userID}.request.rooms.get.{roomID}",
		func(ctx context.Context, p natsrouter.Params, req GreetRequest) (*Room, error) {
			room := findRoom(p.Get("roomID"))
			if room == nil {
				// User-facing error — client receives: {"error":"room not found","code":"not_found"}
				return nil, natsrouter.ErrWithCode("not_found", "room not found")
			}
			return room, nil
			// If findRoom returned a Go error (e.g. DB failure), return it as-is:
			//   return nil, fmt.Errorf("db lookup: %w", err)
			// Client would receive: {"error":"internal error"} (sanitized)
		},
	)
}

func findRoom(_ string) *Room { return nil }

type TypingEvent struct {
	RoomID string `json:"roomId"`
}

// Example_fireAndForget demonstrates RegisterVoid for events with no response.
func Example_fireAndForget() {
	nc, _ := nats.Connect(nats.DefaultURL)
	router := natsrouter.New(nc, "chat-service")

	// No response sent — the sender publishes and moves on.
	natsrouter.RegisterVoid(
		router,
		"chat.user.{userID}.event.typing",
		func(ctx context.Context, p natsrouter.Params, req TypingEvent) error {
			fmt.Printf("user %s is typing in room %s\n", p.Get("userID"), req.RoomID)
			return nil
		},
	)
}

// Example_customMiddleware demonstrates writing custom middleware.
func Example_customMiddleware() {
	nc, _ := nats.Connect(nats.DefaultURL)
	router := natsrouter.New(nc, "my-service")

	// Custom middleware that rejects requests with empty payloads.
	requireBody := func(next nats.MsgHandler) nats.MsgHandler {
		return func(msg *nats.Msg) {
			if len(msg.Data) == 0 {
				msg.Respond([]byte(`{"error":"request body required"}`))
				return
			}
			next(msg)
		}
	}

	router.Use(natsrouter.Recovery())
	router.Use(natsrouter.Middleware(requireBody))

	natsrouter.Register[GreetRequest, GreetResponse](
		router,
		"chat.user.{userID}.greet",
		func(ctx context.Context, p natsrouter.Params, req GreetRequest) (*GreetResponse, error) {
			return &GreetResponse{Reply: "hello"}, nil
		},
	)
}
