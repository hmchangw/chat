package harness

import (
	"fmt"
	"os"
	"strings"
)

// Config holds suite configuration loaded from env vars.
// Per-site URLs use uppercase-site-id suffixes: AUTH_SERVICE_URL_TW.
type Config struct {
	Sites       []string
	PrimarySite string

	authURLs map[string]string
	roomURLs map[string]string
	natsURLs map[string]string
}

// LoadConfig parses env vars and validates that every site has the
// service URLs the v1 suite requires (auth + room).
func LoadConfig() (*Config, error) {
	sitesRaw := strings.TrimSpace(os.Getenv("SITES"))
	if sitesRaw == "" {
		return nil, fmt.Errorf("config: SITES is required (comma-separated list)")
	}
	sites := strings.Split(sitesRaw, ",")
	for i, s := range sites {
		sites[i] = strings.TrimSpace(s)
	}

	primary := strings.TrimSpace(os.Getenv("PRIMARY_SITE"))
	if primary == "" {
		return nil, fmt.Errorf("config: PRIMARY_SITE is required")
	}

	cfg := &Config{
		Sites:       sites,
		PrimarySite: primary,
		authURLs:    map[string]string{},
		roomURLs:    map[string]string{},
		natsURLs:    map[string]string{},
	}

	for _, site := range sites {
		up := strings.ToUpper(site)

		auth := os.Getenv("AUTH_SERVICE_URL_" + up)
		if auth == "" {
			return nil, fmt.Errorf("config: AUTH_SERVICE_URL_%s is required", up)
		}
		cfg.authURLs[site] = auth

		room := os.Getenv("ROOM_SERVICE_URL_" + up)
		if room == "" {
			return nil, fmt.Errorf("config: ROOM_SERVICE_URL_%s is required", up)
		}
		cfg.roomURLs[site] = room

		natsURL := os.Getenv("NATS_URL_" + up)
		if natsURL == "" {
			return nil, fmt.Errorf("config: NATS_URL_%s is required", up)
		}
		cfg.natsURLs[site] = natsURL
	}

	if _, ok := cfg.authURLs[primary]; !ok {
		return nil, fmt.Errorf("config: PRIMARY_SITE %q is not in SITES list", primary)
	}

	return cfg, nil
}

// AuthServiceURL returns the auth-service URL for the given site.
// Panics on unknown site — caller error, not a runtime condition.
func (c *Config) AuthServiceURL(site string) string {
	u, ok := c.authURLs[site]
	if !ok {
		panic(fmt.Sprintf("config: unknown site %q", site))
	}
	return u
}

// RoomServiceURL returns the room-service URL for the given site.
// Panics on unknown site — caller error, not a runtime condition.
func (c *Config) RoomServiceURL(site string) string {
	u, ok := c.roomURLs[site]
	if !ok {
		panic(fmt.Sprintf("config: unknown site %q", site))
	}
	return u
}

// NATSURL returns the NATS broker URL for the given site.
func (c *Config) NATSURL(site string) string {
	u, ok := c.natsURLs[site]
	if !ok {
		panic(fmt.Sprintf("config: unknown site %q", site))
	}
	return u
}
