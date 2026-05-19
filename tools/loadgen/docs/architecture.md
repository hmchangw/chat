# loadgen architecture

Component dependency graph showing the per-run lifecycle wiring introduced
in Phase 0 + Phase 2.

```mermaid
graph LR
    main[main.go runRun] --> rt[Runtime]
    rt --> nc[*nats.Conn]
    rt --> js[jetstream.JetStream]
    rt --> pool[ConnPool]
    rt --> collector[Collector<br/>HDR cells]
    rt --> metrics[Metrics<br/>Prometheus registry]
    rt --> publisher[Publisher]
    rt --> requester[Requester]
    rt --> subscribers[Subscribers<br/>long-lived NATS subs]
    rt --> sites[Sites<br/>len=1 default, 2 federation]
    rt --> runlock[RunLock<br/>loadgen_runs in loadgen_shared]
    rt --> omission[OmissionTracker]

    scenario[Scenario] -. ScenarioDeps .-> rt
    scenario -- NewGenerator --> generator[Runner]
    generator -- publishes via --> publisher
    generator -- reads via --> requester
    generator -- records to --> collector

    finalize[Runtime.Finalize<br/>fresh ctx, 30s timeout] -- WriteBundle --> bundle[runs/<run_id>/<br/>summary.json, histograms.hlog, etc.]
```

## Key invariants

- `Runtime` owns all per-run dependencies. `executeRun` constructs scenarios via `ScenarioDeps`, never via globals.
- `Runtime.Finalize` uses a fresh context (not the signal-cancelled one) so artifact writes survive SIGTERM.
- `RunLock` lives in a SHARED Mongo DB (`loadgen_shared`), not the per-run DB — concurrent runs in different per-run DBs would never see each other otherwise.
- `Subscribers.Close()` drains BEFORE `nc.Drain()` so unsubscribes flush cleanly.
