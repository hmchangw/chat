package server

import (
	"fmt"
	"log"

	"github.com/hmchangw/chat/handler"
	"github.com/hmchangw/chat/store"
	"github.com/nats-io/nats.go"
)

type Config struct {
	NATSUrl string
}

type Server struct {
	nc      *nats.Conn
	handler *handler.Handler
}

func New(cfg Config) (*Server, error) {
	nc, err := nats.Connect(cfg.NATSUrl)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS at %s: %w", cfg.NATSUrl, err)
	}
	log.Printf("connected to NATS at %s", cfg.NATSUrl)

	mem := store.NewMemory()
	h := handler.New(nc, mem)

	return &Server{nc: nc, handler: h}, nil
}

func (s *Server) Start() error {
	return s.handler.Register()
}

func (s *Server) Shutdown() {
	s.nc.Drain()
	log.Println("server shut down")
}
