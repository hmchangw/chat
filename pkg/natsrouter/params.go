package natsrouter

import (
	"fmt"
	"strings"
)

// Params holds named tokens extracted from a NATS subject at request time.
// Values are populated by matching the incoming subject against the registered
// pattern's {name} placeholders.
type Params struct {
	values map[string]string
}

// NewParams creates Params from a map of key-value pairs.
// Useful for testing handlers that accept Params without a real NATS subject.
func NewParams(values map[string]string) Params {
	return Params{values: values}
}

// Get returns the value of a named param, or empty string if not found.
func (p Params) Get(key string) string {
	return p.values[key]
}

// MustGet returns the value of a named param. Panics if the key is not found.
// Use only when the param is guaranteed by the pattern — a panic here means
// the pattern doesn't contain the requested param (developer error).
func (p Params) MustGet(key string) string {
	v, ok := p.values[key]
	if !ok {
		panic(fmt.Sprintf("natsrouter: param %q not found in subject", key))
	}
	return v
}

// Require returns the value of a named param or an error if not found/empty.
func (p Params) Require(key string) (string, error) {
	v, ok := p.values[key]
	if !ok || v == "" {
		return "", ErrBadRequest("missing required param: " + key)
	}
	return v, nil
}

// route is created once at registration time from a pattern.
// It holds the converted NATS wildcard subject and the position-to-name
// mapping for param extraction.
type route struct {
	natsSubject string         // "chat.user.*.request.room.*.*.msg.history"
	params      map[int]string // {2: "userID", 5: "roomID", 6: "siteID"}
}

// parsePattern converts a pattern with {name} placeholders into a route.
// Each {name} token becomes a * in the NATS subject and its position is
// recorded in the params map for extraction at request time.
//
// Example:
//
//	parsePattern("chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history")
//	→ route{
//	    natsSubject: "chat.user.*.request.room.*.*.msg.history",
//	    params:      map[int]string{2: "userID", 5: "roomID", 6: "siteID"},
//	  }
func parsePattern(pattern string) route {
	parts := strings.Split(pattern, ".")
	params := make(map[int]string)
	nats := make([]string, len(parts))

	for i, part := range parts {
		if len(part) > 2 && part[0] == '{' && part[len(part)-1] == '}' {
			name := part[1 : len(part)-1]
			params[i] = name
			nats[i] = "*"
		} else {
			nats[i] = part
		}
	}

	return route{
		natsSubject: strings.Join(nats, "."),
		params:      params,
	}
}

// extractParams splits an incoming subject by "." and pulls values from
// the positions recorded in the route's params map.
func (r route) extractParams(subject string) Params {
	parts := strings.Split(subject, ".")
	values := make(map[string]string, len(r.params))
	for pos, name := range r.params {
		if pos < len(parts) {
			values[name] = parts[pos]
		}
	}
	return Params{values: values}
}
