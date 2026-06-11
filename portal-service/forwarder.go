package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/restyutil"
)

// AuthForwarder posts an auth request body to a site auth-service and returns
// the upstream status and raw body. It never interprets the payload.
type AuthForwarder interface {
	Forward(ctx context.Context, authURL string, body []byte) (status int, respBody []byte, err error)
}

type restyForwarder struct {
	client *resty.Client
}

// Empty base URL: Forward targets a per-site absolute URL on every call.
func newRestyForwarder(timeout time.Duration, tlsSkipVerify bool) *restyForwarder {
	opts := []restyutil.Option{restyutil.WithTimeout(timeout)}
	if tlsSkipVerify {
		opts = append(opts, restyutil.WithTransport(&http.Transport{
			// #nosec G402 -- InsecureSkipVerify is opt-in via TLS_SKIP_VERIFY for dev environments
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec
		}))
	}
	return &restyForwarder{client: restyutil.New("", opts...)}
}

func (f *restyForwarder) Forward(ctx context.Context, authURL string, body []byte) (int, []byte, error) {
	resp, err := f.client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader(natsutil.RequestIDHeader, natsutil.RequestIDFromContext(ctx)).
		SetBody(body).
		Post(strings.TrimRight(authURL, "/") + "/auth")
	if err != nil {
		return 0, nil, fmt.Errorf("post to auth-service: %w", err)
	}
	return resp.StatusCode(), resp.Body(), nil
}
