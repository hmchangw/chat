package atrest

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	resultOK    = "ok"
	resultError = "error"
)

// resultLabel returns the metric label value matching err.
func resultLabel(err error) string {
	if err != nil {
		return resultError
	}
	return resultOK
}

var (
	encryptCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "atrest_encrypt_total",
		Help: "Total payload encryptions, by result.",
	}, []string{"result"})

	decryptCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "atrest_decrypt_total",
		Help: "Total payload decryptions, by result.",
	}, []string{"result"})

	dekCacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "atrest_dek_cache_hits_total",
		Help: "DEK cache hits.",
	})

	dekCacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "atrest_dek_cache_misses_total",
		Help: "DEK cache misses (forced a store fetch or lazy creation).",
	})

	dekCreations = promauto.NewCounter(prometheus.CounterOpts{
		Name: "atrest_dek_creations_total",
		Help: "Lazy DEK creations.",
	})

	kekReloadCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "atrest_kek_reload_total",
		Help: "KEK file reload attempts, by result.",
	}, []string{"result"})

	kekCurrentVersion = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "atrest_kek_current_version",
		Help: "Current KEK version in memory.",
	})
)
