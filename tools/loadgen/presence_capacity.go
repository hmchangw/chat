package main

import (
	"flag"
	"fmt"
	"time"
)

type capacityConfig struct {
	Steps            []int
	Warmup           time.Duration
	Hold             time.Duration
	Cooldown         time.Duration
	Heartbeat        time.Duration
	ConnectP95Ms     float64
	ConnectP99Ms     float64
	FalseOfflineRate float64
	ErrorRate        float64
	PingTolerance    float64
	StopOnTrip       bool
	PublisherConns   int
	ObserverConns    int
	CSVPath          string
}

func parseCapacityConfig(args []string) (capacityConfig, error) {
	fs := flag.NewFlagSet("presence-capacity", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `loadgen presence-capacity — find the max concurrent online population N

Cumulatively ramps a synthetic population through --steps. Each step activates
the delta of new users (each sends hello, measuring connect-edge latency),
holds with every user online and heartbeating, and counts false offlines
(users the service wrongly swept offline) plus ping sustainability. Reports the
largest N held without tripping.

Flags:
`)
		fs.PrintDefaults()
	}
	steps := fs.String("steps", "10000,20000,50000,100000,200000", "comma-separated cumulative N per step; k suffix x1000")
	warmup := fs.Duration("warmup", 30*time.Second, "post-activation settle before snapshot")
	hold := fs.Duration("hold", 120*time.Second, "steady-state false-offline window")
	cooldown := fs.Duration("cooldown", 15*time.Second, "per-step cooldown before next step")
	heartbeat := fs.Duration("heartbeat", 30*time.Second, "per-user ping interval (matches PRESENCE_HEARTBEAT_INTERVAL)")
	connectP95 := fs.Float64("connect-p95-ms", 500, "connect-edge p95 latency cap (ms)")
	connectP99 := fs.Float64("connect-p99-ms", 1000, "connect-edge p99 latency cap (ms)")
	falseOff := fs.Float64("false-offline-rate", 0.001, "false-offline fraction cap (TRIP)")
	errRate := fs.Float64("error-rate", 0.01, "connect error-rate cap (fraction)")
	pingTol := fs.Float64("ping-tolerance", 0.10, "ping-sustainability shortfall band (INCONCLUSIVE)")
	stop := fs.Bool("stop-on-trip", true, "stop the ramp on the first TRIP")
	pub := fs.Int("publisher-conns", 16, "shared publisher connection count")
	obs := fs.Int("observer-conns", 4, "observer connection count (subscribe presence.state.*)")
	csv := fs.String("csv", "", "optional CSV output path")
	if err := fs.Parse(args); err != nil {
		return capacityConfig{}, err
	}
	parsedSteps, err := parseStepList(*steps)
	if err != nil {
		return capacityConfig{}, err
	}
	return capacityConfig{
		Steps: parsedSteps, Warmup: *warmup, Hold: *hold, Cooldown: *cooldown,
		Heartbeat: *heartbeat, ConnectP95Ms: *connectP95, ConnectP99Ms: *connectP99,
		FalseOfflineRate: *falseOff, ErrorRate: *errRate, PingTolerance: *pingTol,
		StopOnTrip: *stop, PublisherConns: *pub, ObserverConns: *obs, CSVPath: *csv,
	}, nil
}
