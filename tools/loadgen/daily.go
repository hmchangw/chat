package main

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// dailyConfig is the parsed CLI input for `loadgen daily`.
type dailyConfig struct {
	Preset             string
	Steps              []int
	Warmup             time.Duration
	Hold               time.Duration
	Cooldown           time.Duration
	StopOnTrip         bool
	MaxDirectUsers     int
	MultiplexPoolSize  int
	MaxConnsPerProcess int
	CSVPath            string
}

func parseDailyConfig(args []string) (dailyConfig, error) {
	fs := flag.NewFlagSet("daily", flag.ContinueOnError)
	preset := fs.String("preset", "daily-heavy", "preset name (daily-light|daily-heavy|daily-power)")
	steps := fs.String("steps", "1000,2000,5000,10000,20000,50000,100000", "comma-separated N values")
	warmup := fs.Duration("warmup", 60*time.Second, "per-step warm-up")
	hold := fs.Duration("hold", 180*time.Second, "per-step hold")
	cooldown := fs.Duration("cooldown", 30*time.Second, "per-step cooldown")
	stopOnTrip := fs.Bool("stop-on-trip", true, "stop on first trip")
	maxDirect := fs.Int("max-direct-users", 20000, "direct-pool cap")
	mux := fs.Int("multiplex-pool-size", 200, "multiplex pool size")
	maxConns := fs.Int("max-conns-per-process", 25000, "safety ceiling on connections")
	csvPath := fs.String("csv", "", "optional CSV output path")
	if err := fs.Parse(args); err != nil {
		return dailyConfig{}, err
	}

	if _, ok := BuiltinPreset(*preset); !ok {
		return dailyConfig{}, fmt.Errorf("unknown preset %q", *preset)
	}

	parsedSteps, err := parseStepList(*steps)
	if err != nil {
		return dailyConfig{}, err
	}

	projected := *maxDirect + *mux
	if projected > *maxConns {
		return dailyConfig{}, fmt.Errorf(
			"projected conn count %d (direct=%d + mux=%d) exceeds --max-conns-per-process=%d",
			projected, *maxDirect, *mux, *maxConns)
	}

	return dailyConfig{
		Preset:             *preset,
		Steps:              parsedSteps,
		Warmup:             *warmup,
		Hold:               *hold,
		Cooldown:           *cooldown,
		StopOnTrip:         *stopOnTrip,
		MaxDirectUsers:     *maxDirect,
		MultiplexPoolSize:  *mux,
		MaxConnsPerProcess: *maxConns,
		CSVPath:            *csvPath,
	}, nil
}

func parseStepList(s string) ([]int, error) {
	if s == "" {
		return nil, fmt.Errorf("--steps cannot be empty")
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		mult := 1
		if strings.HasSuffix(p, "k") {
			mult = 1000
			p = strings.TrimSuffix(p, "k")
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid step %q: %w", p, err)
		}
		out = append(out, n*mult)
	}
	return out, nil
}
