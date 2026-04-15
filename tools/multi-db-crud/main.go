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

// parseLogLevel converts a string (e.g. "debug", "warn") to a slog.Level,
// falling back to Info if the value is unrecognised.
func parseLogLevel(s string) slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return level
}

// newHTTPServer creates an http.Server with sensible timeouts bound to the given port.
func newHTTPServer(port int, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}

// newRouter wires up a new Gin engine with standard middleware and all routes registered.
func newRouter(h *handler, readOnly bool) *gin.Engine {
	r := gin.New()
	r.Use(requestIDMiddleware())
	r.Use(readOnlyMiddleware(readOnly))
	registerRoutes(r, h)
	return r
}

// reaperShutdownFunc returns a shutdown hook that cancels the idle-reaper context when set.
func reaperShutdownFunc(cancel *context.CancelFunc) func(context.Context) error {
	return func(_ context.Context) error {
		if *cancel != nil {
			(*cancel)()
		}
		return nil
	}
}

func main() {
	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	level := parseLogLevel(cfg.LogLevel)
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	reg := newRegistry(time.Duration(cfg.IdleTimeoutMinutes) * time.Minute)
	reaperCtx, reaperCancel := context.WithCancel(context.Background())
	go runReaper(reaperCtx, reg, time.Minute)

	h := newHandler(reg, newMongoOps(), cfg.MaxImportDocs)
	r := newRouter(h, cfg.ReadOnly)
	srv := newHTTPServer(cfg.Port, r)

	slog.Info("multi-db-crud starting", "port", cfg.Port)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	shutdown.Wait(context.Background(), 25*time.Second,
		reaperShutdownFunc(&reaperCancel),
		func(_ context.Context) error {
			reg.CloseAll()
			return nil
		},
		func(ctx context.Context) error {
			return srv.Shutdown(ctx)
		},
	)
}
