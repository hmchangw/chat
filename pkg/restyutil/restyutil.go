// Package restyutil constructs a resty client wired with codebase defaults: OTel transport, slog response logging, 30s timeout.
package restyutil

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-resty/resty/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const defaultTimeout = 30 * time.Second

type Option func(*resty.Client)

func WithTimeout(d time.Duration) Option {
	return func(c *resty.Client) { c.SetTimeout(d) }
}

func WithHeader(key, value string) Option {
	return func(c *resty.Client) { c.SetHeader(key, value) }
}

func WithBearerToken(token string) Option {
	return func(c *resty.Client) { c.SetAuthToken(token) }
}

// WithRetries configures n retries with exponential backoff between wait and max.
func WithRetries(n int, wait, max time.Duration) Option {
	return func(c *resty.Client) {
		c.SetRetryCount(n).SetRetryWaitTime(wait).SetRetryMaxWaitTime(max)
	}
}

func New(baseURL string, opts ...Option) *resty.Client {
	c := resty.New().
		SetBaseURL(baseURL).
		SetTimeout(defaultTimeout).
		SetTransport(otelhttp.NewTransport(http.DefaultTransport))

	c.OnAfterResponse(func(_ *resty.Client, resp *resty.Response) error {
		slog.Debug("http response",
			"method", resp.Request.Method,
			"url", resp.Request.URL,
			"status", resp.StatusCode(),
			"duration_ms", resp.Time().Milliseconds(),
		)
		return nil
	})

	for _, opt := range opts {
		opt(c)
	}
	return c
}
