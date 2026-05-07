package searchengine

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
)

// New creates a SearchEngine for the given backend ("elasticsearch" or "opensearch").
// It verifies connectivity via Ping before returning. When tlsSkipVerify is
// true, server certificate verification is disabled — intended for
// self-signed/internal ES clusters; MUST stay false in production.
func New(ctx context.Context, backend, url string, tlsSkipVerify bool) (SearchEngine, error) {
	var transport Transporter
	switch backend {
	case "elasticsearch":
		esCfg := elasticsearch.Config{Addresses: []string{url}}
		if tlsSkipVerify {
			dt, ok := http.DefaultTransport.(*http.Transport)
			if !ok {
				return nil, fmt.Errorf("create elasticsearch client: http.DefaultTransport is not *http.Transport")
			}
			httpTransport := dt.Clone()
			httpTransport.TLSClientConfig = &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // intentional: opt-in via config for self-signed ES certs
				MinVersion:         tls.VersionTLS12,
			}
			esCfg.Transport = httpTransport
		}
		client, err := elasticsearch.NewClient(esCfg)
		if err != nil {
			return nil, fmt.Errorf("create elasticsearch client: %w", err)
		}
		transport = client
	default:
		return nil, fmt.Errorf("unsupported search backend: %s", backend)
	}

	adapter := newAdapter(transport)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := adapter.Ping(pingCtx); err != nil {
		return nil, fmt.Errorf("search engine ping failed: %w", err)
	}

	return adapter, nil
}
