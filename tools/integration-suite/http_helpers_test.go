package integrationsuite

import (
	"context"
	"time"

	"github.com/go-resty/resty/v2"
)

const httpTimeout = 10 * time.Second //nolint:unused

// newHTTPClient returns a Resty client with a 10s timeout and a
// traceparent generator that runs on every request, recording the
// generated value into world.
func newHTTPClient(w *World) *resty.Client { //nolint:unused
	c := resty.New().
		SetTimeout(httpTimeout).
		SetHeader("Accept", "application/json")

	c.OnBeforeRequest(func(_ *resty.Client, r *resty.Request) error {
		tp := NewTraceparent()
		r.SetHeader(TraceparentHeader, tp)
		// Stash the traceparent on the request context so AfterResponse can read it.
		r.SetContext(withTraceparent(r.Context(), tp))
		return nil
	})

	c.OnAfterResponse(func(_ *resty.Client, r *resty.Response) error {
		tp, _ := traceparentFromContext(r.Request.Context())
		traceID, _ := TraceIDFromTraceparent(tp)
		w.SetLastResponse(&LastResponse{
			StatusCode: r.StatusCode(),
			Body:       r.Body(),
			TraceID:    traceID,
		})
		return nil
	})

	return c
}

type traceparentKey struct{} //nolint:unused

func withTraceparent(ctx context.Context, tp string) context.Context { //nolint:unused
	return context.WithValue(ctx, traceparentKey{}, tp)
}

func traceparentFromContext(ctx context.Context) (string, bool) { //nolint:unused
	v, ok := ctx.Value(traceparentKey{}).(string)
	return v, ok
}
