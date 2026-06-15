package main

import (
	"encoding/json"
	"fmt"
)

// clusterDomain maps a site to its avatar-service base URL (incl. scheme).
type clusterDomain struct {
	SiteID string `json:"siteID"`
	Domain string `json:"domain"`
}

// clusterDomains is parsed from the CLUSTER_DOMAINS env var — a JSON array of
// {siteID, domain} objects. It implements encoding.TextUnmarshaler so
// caarlos0/env populates it directly from the env string (rather than env's
// built-in slice/map splitting).
type clusterDomains struct {
	entries []clusterDomain
}

func (c *clusterDomains) UnmarshalText(text []byte) error {
	if err := json.Unmarshal(text, &c.entries); err != nil {
		return fmt.Errorf("parse CLUSTER_DOMAINS json: %w", err)
	}
	return nil
}

// baseURL returns the configured base URL for a site, or "" if not configured.
func (c clusterDomains) baseURL(siteID string) string {
	for _, e := range c.entries {
		if e.SiteID == siteID {
			return e.Domain
		}
	}
	return ""
}

type config struct {
	Port     string `env:"PORT" envDefault:"8080"`
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
	SiteID   string `env:"SITE_ID,required"`

	// CLUSTER_DOMAINS is a JSON array of {siteID, domain} objects mapping each
	// site to that cluster's avatar-service base URL (incl. scheme), used
	// verbatim as a cross-cluster redirect target.
	ClusterDomains clusterDomains `env:"CLUSTER_DOMAINS,required"`

	EmployeePhotoBaseURL string `env:"EMPLOYEE_PHOTO_BASE_URL,required"`

	MongoURI      string `env:"MONGO_URI,required"`
	MongoDB       string `env:"MONGO_DB" envDefault:"chat"`
	MongoUsername string `env:"MONGO_USERNAME"`
	MongoPassword string `env:"MONGO_PASSWORD"`

	MinioEndpoint  string `env:"MINIO_ENDPOINT,required"`
	MinioAccessKey string `env:"MINIO_ACCESS_KEY,required"`
	MinioSecretKey string `env:"MINIO_SECRET_KEY,required"`
	MinioUseSSL    bool   `env:"MINIO_USE_SSL" envDefault:"false"`
	AvatarBucket   string `env:"AVATAR_BUCKET" envDefault:"avatars"`

	MaxUploadBytes     int64 `env:"MAX_UPLOAD_BYTES" envDefault:"1048576"`
	CacheMaxAgeSeconds int   `env:"CACHE_MAX_AGE_SECONDS" envDefault:"21600"`
}

// clusterBaseURL returns the configured base URL for a site, or "" if unknown.
func (c *config) clusterBaseURL(siteID string) string { return c.ClusterDomains.baseURL(siteID) }
