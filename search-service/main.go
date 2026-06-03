package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
	"github.com/caarlos0/env/v11"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/atrest"
	"github.com/hmchangw/chat/pkg/blindidx"
	"github.com/hmchangw/chat/pkg/blindsearch"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/restyutil"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/searchindex"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/valkeyutil"
)

// ESConfig bundles the search backend knobs. BACKEND is the key
// `pkg/searchengine.New` reads to choose between elasticsearch/opensearch.
type ESConfig struct {
	URL           string `env:"URL,required"`
	Backend       string `env:"BACKEND"          envDefault:"elasticsearch"`
	Username      string `env:"USERNAME"         envDefault:""`
	Password      string `env:"PASSWORD"         envDefault:""`
	TLSSkipVerify bool   `env:"TLS_SKIP_VERIFY"  envDefault:"false"`
}

type ValkeyConfig struct {
	Addrs    []string `env:"ADDRS,required" envSeparator:","`
	Password string   `env:"PASSWORD"        envDefault:""`
}

type NATSConfig struct {
	URL       string `env:"URL,required"`
	CredsFile string `env:"CREDS_FILE" envDefault:""`
}

type MongoConfig struct {
	URI      string `env:"URI,required"`
	DB       string `env:"DB"       envDefault:"chat"`
	Username string `env:"USERNAME" envDefault:""`
	Password string `env:"PASSWORD" envDefault:""`
}

// UsersAPIConfig carries the third-party HR endpoint settings.
// URL is required; Token is optional (TBD when the third-party auth scheme
// is known — see TODO(searchUsers-thirdparty) in users_client.go).
type UsersAPIConfig struct {
	URL     string        `env:"URL,required"`
	Timeout time.Duration `env:"TIMEOUT" envDefault:"5s"`
	Token   string        `env:"TOKEN"   envDefault:""`
}

// SearchConfig groups the request-shape knobs — size caps, cache TTL, and
// the recent-window filter bound. All optional with sane defaults so a
// minimal environment only needs URL + NATS_URL + VALKEY_ADDRS.
type SearchConfig struct {
	DocCounts               int           `env:"DOC_COUNTS"                 envDefault:"25"`
	MaxDocCounts            int           `env:"MAX_DOC_COUNTS"             envDefault:"100"`
	RestrictedRoomsCacheTTL time.Duration `env:"RESTRICTED_ROOMS_CACHE_TTL" envDefault:"5m"`
	RecentWindow            time.Duration `env:"RECENT_WINDOW"              envDefault:"8760h"`
	RequestTimeout          time.Duration `env:"REQUEST_TIMEOUT"            envDefault:"10s"`
	UserRoomIndex           string        `env:"USER_ROOM_INDEX,required"`
	SpotlightIndex          string        `env:"SPOTLIGHT_INDEX,required"`
	MetricsAddr             string        `env:"METRICS_ADDR"               envDefault:":9090"`
}

// EncConfig groups the flag-gated encrypted-search knobs. It shares the
// `SEARCH_` env prefix with ESConfig/SearchConfig — its fields (ENC_*,
// BENCH_MODE_ENABLED) don't collide with theirs today; any new field added to
// any of the three SEARCH_-prefixed structs must be checked against the others
// to avoid silent env shadowing (see the Config foot-gun note).
type EncConfig struct {
	Enabled          bool   `env:"ENC_ENABLED"          envDefault:"false"`
	DefaultArm       string `env:"ENC_DEFAULT_ARM"      envDefault:"C"`
	MsgIndexPrefix   string `env:"ENC_MSG_INDEX_PREFIX" envDefault:""`
	BenchModeEnabled bool   `env:"BENCH_MODE_ENABLED"   envDefault:"false"`
}

// BlindConfig holds the blind-index HMAC key + version used to tokenize the
// query the same way the enc index was written. Required only when encryption
// is enabled. Env names are absolute (BLINDIDX_*) so they match the
// search-sync-worker that produces the index.
type BlindConfig struct {
	Key     string `env:"BLINDIDX_KEY"         envDefault:""`
	Version string `env:"BLINDIDX_KEY_VERSION" envDefault:""`
}

// Config is the root service config. Note that ES, Search and Enc share the
// `SEARCH_` env prefix — the fields on those structs don't collide today, but
// any new field added to any of them must be checked against the others or
// moved to a distinct prefix to avoid silent env shadowing.
type Config struct {
	SiteID   string             `env:"SITE_ID,required"`
	ES       ESConfig           `envPrefix:"SEARCH_"`
	Valkey   ValkeyConfig       `envPrefix:"VALKEY_"`
	NATS     NATSConfig         `envPrefix:"NATS_"`
	Search   SearchConfig       `envPrefix:"SEARCH_"`
	Enc      EncConfig          `envPrefix:"SEARCH_"`
	Blind    BlindConfig        ``
	Atrest   atrest.Config      ``
	Vault    atrest.VaultConfig ``
	Mongo    MongoConfig        `envPrefix:"MONGO_"`
	UsersAPI UsersAPIConfig     `envPrefix:"USERS_API_"`
}

// encDeps bundles the constructed encrypted-search dependencies handed to the
// handler. All fields are zero/nil on a plaintext-only (arm-C) deployment.
type encDeps struct {
	hasher        *blindidx.Hasher
	cipher        decrypter
	historyClient historyBatchClient
	indexPattern  []string
}

// validateEncConfig validates the flag-gated encrypted-search config WITHOUT
// constructing any infra (Vault/Mongo/NATS), so each validation branch is unit
// testable. buildEncDeps calls it first, then does the infra wiring. The
// DefaultArm is read from enc.DefaultArm (already uppercased by the caller in
// main.go before this runs in production; the test exercises the raw struct).
//
// Rules: when encryption is disabled the only valid default arm is C; when
// enabled the arm must be one of C/A/B, the index prefix must carry a -v<N>
// suffix, and the blind key must decode to a long-enough HMAC key.
func validateEncConfig(enc EncConfig, blind BlindConfig) error {
	defaultArm := strings.ToUpper(strings.TrimSpace(enc.DefaultArm))

	if !enc.Enabled {
		if defaultArm != armC {
			return fmt.Errorf("SEARCH_ENC_DEFAULT_ARM=%q requires SEARCH_ENC_ENABLED=true", defaultArm)
		}
		return nil
	}

	switch defaultArm {
	case armC, armA, armB:
	default:
		return fmt.Errorf("invalid SEARCH_ENC_DEFAULT_ARM %q: must be one of C, A, B", defaultArm)
	}

	if _, _, ok := searchindex.StripVersion(enc.MsgIndexPrefix); !ok {
		return fmt.Errorf("invalid SEARCH_ENC_MSG_INDEX_PREFIX %q: must end with -v<N>, e.g. enc-messages-v1", enc.MsgIndexPrefix)
	}

	if _, err := blindsearch.LoadHasher(blind.Key, blind.Version); err != nil {
		return fmt.Errorf("load blind hasher: %w", err)
	}

	return nil
}

// buildEncDeps validates the enc config and constructs the dependencies each
// honored arm needs, failing fast on a misconfiguration. The hasher is always
// required when encryption is on; the cipher is required if any honored arm is
// A; the history client if any honored arm is B. "Honored" = the default arm
// plus (in bench mode) every variant a request may select, so a bench-mode
// deployment must wire BOTH A and B.
func buildEncDeps(ctx context.Context, cfg *Config, mongoDB *mongo.Database, nc *otelnats.Conn, defaultArm string) (encDeps, error) {
	if err := validateEncConfig(cfg.Enc, cfg.Blind); err != nil {
		return encDeps{}, err
	}

	if !cfg.Enc.Enabled {
		return encDeps{}, nil
	}

	// In bench mode any of A/B may be selected per request, so both content
	// retrievers must be available regardless of the default arm.
	needA := defaultArm == armA || cfg.Enc.BenchModeEnabled
	needB := defaultArm == armB || cfg.Enc.BenchModeEnabled

	base, _, _ := searchindex.StripVersion(cfg.Enc.MsgIndexPrefix)
	indexPattern := []string{fmt.Sprintf("%s-*", base), fmt.Sprintf("*:%s-*", base)}

	hasher, err := blindsearch.LoadHasher(cfg.Blind.Key, cfg.Blind.Version)
	if err != nil {
		return encDeps{}, fmt.Errorf("load blind hasher: %w", err)
	}

	deps := encDeps{hasher: hasher, indexPattern: indexPattern}

	if needA {
		wrapper, werr := atrest.NewVaultKeyWrapper(ctx, cfg.Vault)
		if werr != nil {
			return encDeps{}, fmt.Errorf("construct Vault key wrapper for arm A: %w", werr)
		}
		dekColl := mongoDB.Collection(atrest.CollectionName)
		deps.cipher = atrest.NewCipher(wrapper, atrest.NewMongoDEKStore(dekColl), cfg.Atrest)
	}
	if needB {
		deps.historyClient = newNATSHistoryBatchClient(nc, cfg.SiteID)
	}

	return deps, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[Config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	spotlightBase, _, ok := searchindex.StripVersion(cfg.Search.SpotlightIndex)
	if !ok {
		slog.Error("invalid config", "name", "SEARCH_SPOTLIGHT_INDEX", "value", cfg.Search.SpotlightIndex, "reason", "must end with -v<N>, e.g. spotlight-site-a-v1")
		os.Exit(1)
	}
	spotlightReadPattern := fmt.Sprintf("%s-*", spotlightBase)

	ctx := context.Background()

	tracerShutdown, err := otelutil.InitTracer(ctx, "search-service")
	if err != nil {
		slog.Error("init tracer failed", "error", err)
		os.Exit(1)
	}

	engine, err := searchengine.New(ctx, searchengine.Config{
		Backend:       cfg.ES.Backend,
		URL:           cfg.ES.URL,
		Username:      cfg.ES.Username,
		Password:      cfg.ES.Password,
		TLSSkipVerify: cfg.ES.TLSSkipVerify,
	})
	if err != nil {
		slog.Error("search engine connect failed", "error", err)
		os.Exit(1)
	}

	valkey, err := valkeyutil.ConnectCluster(ctx, cfg.Valkey.Addrs, cfg.Valkey.Password)
	if err != nil {
		slog.Error("valkey connect failed", "error", err)
		os.Exit(1)
	}

	nc, err := natsutil.Connect(cfg.NATS.URL, cfg.NATS.CredsFile)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}

	mongoClient, err := mongoutil.Connect(ctx, cfg.Mongo.URI, cfg.Mongo.Username, cfg.Mongo.Password)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}
	mongoDB := mongoClient.Database(cfg.Mongo.DB)

	usersRC := restyutil.New(
		cfg.UsersAPI.URL,
		restyutil.WithTimeout(cfg.UsersAPI.Timeout),
	)
	usersClient := newHTTPUsersClient(usersRC, cfg.UsersAPI.Token)

	// Resolve the effective default arm: C whenever encryption is disabled,
	// otherwise the configured SEARCH_ENC_DEFAULT_ARM.
	defaultArm := armC
	if cfg.Enc.Enabled {
		defaultArm = strings.ToUpper(cfg.Enc.DefaultArm)
	}

	enc, err := buildEncDeps(ctx, &cfg, mongoDB, nc, defaultArm)
	if err != nil {
		slog.Error("enc search wiring failed", "error", err)
		os.Exit(1)
	}

	store := newESStore(engine, cfg.Search.UserRoomIndex)
	cache := newValkeyCache(valkey)
	mongoStore := newMongoStore(mongoDB)
	handler := newHandler(store, mongoStore, usersClient, cache, &handlerConfig{
		SiteID:                  cfg.SiteID,
		DocCounts:               cfg.Search.DocCounts,
		MaxDocCounts:            cfg.Search.MaxDocCounts,
		RestrictedRoomsCacheTTL: cfg.Search.RestrictedRoomsCacheTTL,
		RecentWindow:            cfg.Search.RecentWindow,
		RequestTimeout:          cfg.Search.RequestTimeout,
		UserRoomIndex:           cfg.Search.UserRoomIndex,
		SpotlightReadPattern:    spotlightReadPattern,
		EncDefaultArm:           defaultArm,
		BenchModeEnabled:        cfg.Enc.BenchModeEnabled,
		EncIndexPattern:         enc.indexPattern,
	})
	handler.hasher = enc.hasher
	handler.cipher = enc.cipher
	handler.historyClient = enc.historyClient

	router := natsrouter.New(nc, "search-service")
	router.Use(natsrouter.RequestID())
	router.Use(natsrouter.Recovery())
	router.Use(natsrouter.Logging())
	handler.Register(router)

	// /metrics-only listener. All four timeouts guard against hung
	// scrapers tying up a goroutine indefinitely on an operator-exposed
	// port.
	//
	// Bind synchronously so a port conflict fails startup loudly —
	// otherwise ListenAndServe's error would surface in a goroutine and
	// the service would run happily with no /metrics, silently losing
	// observability. Serve(listener) takes ownership of the listener
	// from here on; Shutdown() closes it.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metricsHandler())
	metricsServer := &http.Server{
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	metricsListener, err := net.Listen("tcp", cfg.Search.MetricsAddr)
	if err != nil {
		slog.Error("metrics server listen failed", "addr", cfg.Search.MetricsAddr, "error", err)
		os.Exit(1)
	}
	go func() {
		slog.Info("metrics server listening", "addr", cfg.Search.MetricsAddr)
		if err := metricsServer.Serve(metricsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server failed", "error", err)
		}
	}()

	slog.Info("search-service running",
		"site", cfg.SiteID,
		"backend", cfg.ES.Backend,
		"valkey", cfg.Valkey.Addrs,
	)

	shutdown.Wait(ctx, 25*time.Second,
		// Wait for in-flight handlers BEFORE nc.Drain so they can't touch torn-down deps.
		func(ctx context.Context) error { return router.Shutdown(ctx) },
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { return tracerShutdown(ctx) },
		func(_ context.Context) error { valkeyutil.Disconnect(valkey); return nil },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
		// /metrics last so Prometheus can scrape the final drain-window observations.
		func(ctx context.Context) error { return metricsServer.Shutdown(ctx) },
	)
}
