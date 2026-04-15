package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/hmchangw/chat/pkg/shutdown"
)

type config struct {
	Port               int    `env:"PORT"                 envDefault:"8091"`
	IdleTimeoutMinutes int    `env:"IDLE_TIMEOUT_MINUTES" envDefault:"30"`
	MaxImportDocs      int    `env:"MAX_IMPORT_DOCS"      envDefault:"10000"`
	ReadOnly           bool   `env:"READ_ONLY"            envDefault:"false"`
	LogLevel           string `env:"LOG_LEVEL"            envDefault:"info"`
}

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader("X-Request-ID")
		if reqID == "" {
			reqID = uuid.New().String()
		}
		c.Set("reqID", reqID)
		c.Header("X-Request-ID", reqID)

		start := time.Now()
		c.Next()

		slog.Info("request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency", time.Since(start).String(),
			"reqID", reqID,
		)
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	// TODO: initialize connection registry when implemented
	// cancelReaper will cancel the reaper goroutine context when shutting down.
	_, cancelReaper := context.WithCancel(context.Background())
	defer cancelReaper()

	h := newHandler()

	r := gin.New()
	r.Use(requestIDMiddleware())
	registerRoutes(r, h)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	slog.Info("multi-db-crud starting", "port", cfg.Port)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	shutdown.Wait(context.Background(), 25*time.Second,
		func(_ context.Context) error {
			cancelReaper()
			return nil
		},
		func(_ context.Context) error {
			// TODO: registry.CloseAll() when connection registry is implemented
			return nil
		},
		func(ctx context.Context) error {
			return srv.Shutdown(ctx)
		},
	)
}
