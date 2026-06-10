package main

//nolint:unused // wired in a later task
type config struct {
	Port     string `env:"PORT" envDefault:"8080"`
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
	SiteID   string `env:"SITE_ID,required"`

	// CLUSTER_DOMAINS maps siteID → that cluster's avatar-service base URL
	// (incl. scheme), used verbatim as a redirect target.
	ClusterDomains map[string]string `env:"CLUSTER_DOMAINS,required" envKeyValSeparator:"=" envSeparator:","`

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
func (c config) clusterBaseURL(siteID string) string { return c.ClusterDomains[siteID] }
