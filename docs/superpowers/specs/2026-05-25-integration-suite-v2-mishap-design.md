# Integration Suite v2 — Mishap Subsystem (Part-2) Design

**Status:** Draft, supersedes section 3.5 of the parent v2 design for
the Part-2 build-out.
**Parent design:** `docs/superpowers/specs/2026-05-21-integration-suite-v2-design.md`
**Part-1 corrections:** `docs/superpowers/specs/2026-05-24-integration-suite-v2-part1-corrections-design.md`
**Code today:** `tools/integration-suite-v2/internal/mishap/` (types + power-set expander + `ConcurrentExecutor` stub — both replaced by this spec)
**Owns:** the three-mishap-kind catalog (per-pod and sequence-level),
the executor contract, the nested-loop case expansion derived from
each scenario's sequence + ability profiles, and the reporting
changes that surface mishap outcomes.

---

## 1. Purpose and non-goals

### Purpose

A mishap is a **perturbation** the platform applies during a single
test case. The scenario's `reads:` block stays authoritative; a
mishap-applied case passes if the system still produces the expected
reads (with nothing extra) and fails if it doesn't. The mishap
subsystem is the v2 platform's resilience-and-correctness multiplier:
one authored scenario expands across a bounded set of perturbation
cases via the §4.3 loop, and any case that breaks the reads is a real
defect.

Part-2 ships **three mishap kinds** — `crash`, `mongo-pause-500ms`,
and `concurrent-fan-out-3` — covering the project's stated priorities
(idempotency > concurrency > resilience). Per-pod kinds fan out
across pods in each scenario's `sequence`; the sequence-level kind
adds one binary axis. The per-scenario case count is the product of
the loop axes; expansion is mechanical (§4.3).

### Non-goals

- **Not a chaos framework.** No long-lived chaos schedules, no
  cluster-level fault injection, no probabilistic faults. Each mishap
  applies during a single case's timeframe and cleans up before the
  next case.
- **Not infrastructure ownership.** Mishaps that touch containers
  (`docker pause`, `docker restart`) act on the **already-running**
  docker-local stack the suite is pointed at. The suite does not
  start, stop, or scale services.
- **Not a substitute for unit tests.** Mishaps test assembled-system
  behavior under perturbation, not internal logic.
- **Not author-extensible.** Mishaps live in a closed Go catalog,
  reviewed and added deliberately. Scenarios opt-out via
  `mishaps.ignore` but cannot define new mishaps inline.

---

## 2. The three Part-2 mishap kinds

Part-2 ships **three kinds** of mishap — not four named instances.
Two kinds fan out across pods in the scenario's `sequence`; one is a
sequence-level toggle. Case expansion (§4.3) walks all three axes as
a nested loop; each combination is one case.

| Kind                   | Scope (per scenario)                                                                     | Fires when…                                                              | Class       |
|------------------------|------------------------------------------------------------------------------------------|--------------------------------------------------------------------------|-------------|
| `crash`                | every pod in `scenario.sequence` (each is a separate case)                                | the first in-trace event owned by the target pod arrives at the observer | idempotency |
| `mongo-pause-500ms`    | every pod in `scenario.sequence` whose ability profile reads/writes Mongo                | same trigger — first in-trace event owned by the target pod              | resilience  |
| `concurrent-fan-out-3` | sequence-level; one binary axis (off / on)                                                | at `observer.Start`, before the verb fires                               | concurrency |

Per-kind reasoning:

- **`crash`** = `docker restart` of the target pod. Triggered ON the
  pod's first in-trace event, mid-flight, so we exercise the
  in-process recovery path (durable consumer state, mid-write Mongo
  state, JetStream redelivery). Targets every pod in the sequence —
  a 3-pod cascade gives 3 crash cases, each probing a different
  recovery point.
- **`mongo-pause-500ms`** = `docker pause mongodb` for 500ms,
  triggered when a request reaches a Mongo-using pod. Tests two
  properties at once: driver-layer retry/timeout (single-request
  path) and ordering under contention (concurrent path — slow writes
  shuffle commit order; does the system stay consistent?). The
  *paused container* is always `mongodb`; the *trigger pod* is the
  Mongo-using pod that the case targets.
- **`concurrent-fan-out-3`** = three independent traces of the same
  scenario, fired in parallel from three distinct cast fixtures.
  Doesn't target a pod; orthogonal to the per-pod axes. Under
  concurrent=on + a per-pod mishap, each actor's trace gets its
  **own** perturbation, fired once on that actor's first arrival
  at the target — three perturbations per case, one per trace. The
  "once" rule (§2.2) is per-trace: retries WITHIN an actor's trace
  do not re-fire that actor's perturbation.

### 2.1 Per-pod vs sequence-level

The two **categories** are the deep structural distinction. The
expansion machinery (§4.3) and the executor contract (§3) only care
which category a kind is in — not which specific kind. Adding a
fourth mishap kind is one of:

- **Per-pod kind** (`slow-disk`, `network-flap`, `clock-skew`): adds
  one loop dimension; loop indices are `pods_in_seq` or some
  predicate-filtered subset of it. Eligibility comes from an ability
  profile predicate.
- **Sequence-level kind** (`replay-attack`, `duplicate-publish`):
  adds one binary loop dimension; no pod targeting.

Both reuse the same `Executor` interface (§3). The composition-filter
table from the prior draft is gone — the loop structure replaces it.

### 2.2 Misfortune, not adversary — one perturbation per trace

**A mishap fires exactly once per logical request flow (per trace).**
This is the rule that gives mishaps their meaning. The trigger channel
is one-shot per trace: the first event matching `(traceID, target pod)`
fires it; every subsequent matching event on the same trace — a NAK
redelivery, an internal retry within a service, an OTel-propagated
follow-up — is observed by the classifier but does NOT re-fire that
trace's perturbation.

The reason is the property under test. Suppose a NATS request
crashes its receiver. The system should NAK, the broker should
redeliver, and the redelivery should succeed. **That redelivery IS
the behavior we're measuring.** If we crashed the pod again on the
redelivery, the loop would never terminate and the case would always
fail — testing nothing.

Under `c=1`, each actor has its **own** trace and therefore its
**own** perturbation, each fired once on that actor's first arrival
at the target pod. Three actors → three perturbations per case, one
per trace. They reach the pod at slightly different times, so each
actor's cascade is independently observed and gives independent
insight. **Triggers are per-actor, not shared.**

Mishaps model **misfortunes** — transient bad events the system is
supposed to absorb. They are not **adversaries** — they do not
sustain pressure on a single logical request or react to that
request's recovery attempts. A future v3 might add adversarial fault
injection as a separate category; that's explicitly out of scope
here.

Implementation: the runner allocates one trigger channel per
`(traceID, target pod)` tuple. The observer's
`OnFirstInScope(traceID, ownerPod, ch)` registration is one-shot —
it fires on the first match and deregisters itself. An `Apply`
goroutine reading from a closed channel completes silently if no
match ever arrived (e.g. the cascade died upstream of the target —
see §7.5).

**`nats-pause` rejected.** Pausing NATS while a case is in flight
kills the suite's own request/reply path; the case fails for reasons
that aren't about the system under test. Out of scope for Part-2.

**Why one crash kind instead of restart + pause as siblings.** Part-2
ships only crash-restart (SIGTERM + cold start). Crash-pause
(in-flight pause without restart) is a candidate sibling for Part-2.5
once we have a signal that in-flight-pause vs cold-start divergence
matters. The loop structure trivially extends — a sibling kind is
one more value on the per-pod axis, not a new axis.

---

## 3. Executor contract (Go)

The existing `internal/mishap/types.go` defines a minimal `Executor`
interface. Part-2 keeps the `Apply`/`Cleanup` shape but adds a
trigger channel so per-pod mishaps can fire at the moment the
request reaches their target.

### 3.1 Interface

```go
package mishap

import "context"

// Executor performs a perturbation during a single test case.
//
// Lifecycle inside the runner's runOneCase:
//   1. runner constructs the Executor with a (kind, target?) tuple
//      derived from the case's loop indices (§4.3).
//   2. runner allocates ONE trigger channel PER (traceID, target pod)
//      for per-pod executors (nil for sequence-level kinds). c=0
//      has 1 trace → 1 channel. c=1 has 3 traces → 3 channels per
//      per-pod executor. Each channel is one-shot: observer
//      sends-and-closes on the FIRST in-scope event matching its
//      (traceID, target pod).
//   3. observer.Start fires at T_open.
//   4. dispatcher fires the verb (or — for concurrent — Apply does).
//   5. runner calls Apply(ctx, trigger) once per (executor, trace),
//      on a dedicated goroutine. Each Apply BLOCKS on its <-trigger
//      then runs its perturbation EXACTLY ONCE. Subsequent matching
//      events ON THE SAME TRACE (NAK redeliveries, internal retries)
//      flow through the classifier but never re-fire — see §2.2.
//   6. GatherUntilQuiet drains events until the cascade goes quiet.
//   7. runner calls Cleanup(ctx) for each Apply in a deferred block.
//      MUST be idempotent (multiple Apply goroutines may share an
//      underlying docker action — e.g. three actors crashing the
//      same pod is three sequential restart calls, each Cleanup-safe).
type Executor interface {
    Apply(ctx context.Context, trigger <-chan struct{}) error
    Cleanup(ctx context.Context) error
}

// Mishap is the catalog entry. One per YAML file.
type Mishap struct {
    Name     string            // "crash" | "mongo-pause-500ms" | "concurrent-fan-out-3"
    Kind     string            // "per-pod" | "sequence-level"
    Class    string            // "idempotency" | "concurrency" | "resilience"
    Eligible PodPredicate      // for per-pod kinds: which pods fan out;
                               // nil for sequence-level
    Factory  Factory
}

// PodPredicate decides whether a per-pod kind applies to a given pod.
//   crash:              every pod (returns true)
//   mongo-pause-500ms:  pods whose ability profile reads/writes Mongo
type PodPredicate func(pod ServiceProfile) bool

// Factory builds an Executor for one case. For per-pod kinds the
// runner passes the resolved target pod; for sequence-level kinds
// targetPod is the empty string.
type Factory func(fctx FactoryContext, targetPod string) (Executor, error)

// FactoryContext is the slice of per-case state every Factory needs.
type FactoryContext struct {
    Scenario   ResolvedScenario     // resolved placeholders + sequence
    Services   ServiceProfileLookup // catalogs/services/*.yaml registry
    Cast       Cast                 // seeded fixture cast
    DockerCLI  DockerCLI            // docker exec / pause / restart wrapper
    Dispatcher Dispatcher           // for concurrent fan-out fire
}
```

The three Part-2 executors live in `internal/mishap/`, one file
each:

```
internal/mishap/
  types.go         Executor, Mishap, Factory, FactoryContext, PodPredicate
  registry.go      Register(name, Mishap) + Lookup(name) + Kinds()
  expander.go      ExpandCases(scenario, services) → []Case   (replaces PowerSet)
  crash.go         crashExecutor{target} + crashFactory
  mongo_pause.go   mongoPauseExecutor{watchPod, container, pauseMs} + factory
  concurrent.go    concurrentFanOutExecutor{n, scenario, dispatcher} + factory
  docker.go        DockerCLI interface + dockerExec implementation
```

### 3.2 Per-kind mechanics

#### 3.2.1 `crash`

```
Apply(ctx, trigger):
    select { case <-trigger: case <-ctx.Done(): return ctx.Err() }
    return docker.Restart(ctx, target)          // ~1–3s; returns when up

Cleanup(ctx):
    docker.UnpauseIfPaused(ctx, target)
    docker.StartIfStopped(ctx, target)
    return nil
```

The trigger fires when the observer sees the first in-scope event
on this trace whose owner is `target`. At that instant, target was
just doing work on this actor's request — `docker restart` cuts it
off mid-flight, exercising the in-process recovery path.

Under `concurrent=on`, each actor has its own trace and its own
trigger. Three actors → three sequential `docker restart` calls,
each fired when its actor's first event lands on the target. The
calls overlap in time but the restarts serialize (docker queues
them); the *property* under test is "each actor's request flow
survives a restart of the pod handling it." Each actor's retries
within its own trace do NOT re-fire that actor's restart (§2.2).
Some actors will arrive at a pod that's mid-restart due to a sibling
actor's earlier trigger — that's the production-shaped scenario
and we measure it directly.

Notes:
- `docker restart` issues SIGTERM with a 10s grace; chat services
  drain in-flight messages first. To test SIGKILL paths, add a
  `crash-kill` sibling kind (see §7).
- The container's restart policy is `unless-stopped` in docker-local,
  so a manual `docker restart` is a true cold start.

#### 3.2.2 `mongo-pause-500ms`

```
Apply(ctx, trigger):
    select { case <-trigger: case <-ctx.Done(): return ctx.Err() }
    if err := docker.Pause(ctx, "mongodb"); err != nil { return err }
    defer func() { _ = docker.Unpause(context.Background(), "mongodb") }()
    select {
    case <-time.After(500 * time.Millisecond):
    case <-ctx.Done(): return ctx.Err()
    }
    return docker.Unpause(ctx, "mongodb")

Cleanup(ctx):
    docker.UnpauseIfPaused(ctx, "mongodb")
    return nil
```

Two pods/containers are involved per executor:
- **`watchPod`** = the trigger pod, a Mongo-using service in the
  sequence (filled by the runner per the loop index).
- **`container` = `"mongodb"`** = what gets paused (always literal).

When the first in-scope event on this trace owned by `watchPod`
arrives, we pause Mongo for 500ms. The pod tries its Mongo write/read
mid-pause and exercises the driver retry/timeout path; if it NAKs,
the redelivery must succeed against a Mongo that's no longer paused
(and we do NOT re-pause on redelivery within the same trace — §2.2).

Under `concurrent=on`, each actor has its own trigger and its own
500ms pause, fired when that actor's request reaches `watchPod`.
Three actors → three pause-500ms calls. The pauses overlap in
time, and concurrent `docker pause`/`unpause` on the same container
is idempotent — Mongo stays paused for the union of the three
windows. The *property* under test is "do concurrent writes
preserve order/correctness when Mongo is briefly unavailable when
each actor's write attempt happens." Each actor's retries within
its own trace do NOT re-fire its own pause.

#### 3.2.3 `concurrent-fan-out-3`

```
Factory(fctx, _):
    actors := fctx.Cast.FindByPredicate(fctx.Scenario.RequesterPredicate, n=3)
    if len(actors) < 3 { return nil, ErrInsufficientCast }
    return &concurrentFanOut{
        actors: actors,
        scenario: fctx.Scenario,
        dispatcher: fctx.Dispatcher,
    }, nil

Apply(ctx, _):
    // trigger is nil — sequence-level kind, runs at observer.Start
    var wg sync.WaitGroup
    errs := make(chan error, len(c.actors))
    for _, actor := range c.actors {
        wg.Add(1)
        go func(a CastUser) {
            defer wg.Done()
            errs <- c.dispatcher.Fire(ctx, c.scenario, a)  // fresh traceparent per actor
        }(actor)
    }
    wg.Wait(); close(errs)
    return collect(errs)

Cleanup(ctx):
    return nil
```

Each parallel fire gets its own traceparent. The observer keys events
by traceID, so the three cascades stay disjoint in `Classify`.

Cast sizing: a scenario whose placeholder predicate doesn't match
≥3 fixtures FAILS preflight loudly — the `c=1` row of the loop is
skipped for that scenario. Cast size for any predicate referenced by
a concurrency-eligible scenario MUST be ≥3 (mirrors the parent v2
design).

### 3.3 The underlying toolkit — fundamental and scenario-invariant

Everything in §3.2 ultimately composes a **small, fixed set of
primitives**. Scenarios do not add primitives to this set; new
scenarios are configurations of existing primitives, full stop.
Adding a **new mishap kind** may add a method to one of these
primitives or use the existing surface — but that's a rare, deliberate
extension, not a per-scenario growth path.

The toolkit:

| Primitive             | Type / signature                                          | Provided by             | Used by                                                                          |
|-----------------------|-----------------------------------------------------------|-------------------------|----------------------------------------------------------------------------------|
| `DockerCLI.Restart`   | `func(ctx, container string) error`                       | `internal/docker`       | `crash.Apply`                                                                    |
| `DockerCLI.Pause`     | `func(ctx, container string) error`                       | `internal/docker`       | `mongo-pause-500ms.Apply`                                                        |
| `DockerCLI.Unpause`   | `func(ctx, container string) error`                       | `internal/docker`       | `mongo-pause-500ms.Apply`                                                        |
| `DockerCLI.StartIfStopped`   | `func(ctx, container string) error`                | `internal/docker`       | `crash.Cleanup` (idempotent restart-safety)                                      |
| `DockerCLI.UnpauseIfPaused`  | `func(ctx, container string) error`                | `internal/docker`       | `mongo-pause-500ms.Cleanup` (idempotent unpause-safety)                          |
| `Dispatcher.Fire`     | `func(ctx, scenario) (resp, error)`                       | `internal/dispatcher`   | Happy case + every c=0 mishap (single-actor verb fire). Already exists in Part-1.|
| `Dispatcher.FireWithActor` | `func(ctx, scenario, actor) (resp, error)`           | `internal/dispatcher`   | `concurrent-fan-out-3.Apply` (one call per goroutine, 3 actors in parallel).     |
| `Observer.OnFirstInScope` | `func(traceID, ownerPod string, ch chan<- struct{})`  | `internal/observer`     | Runner registers per `(traceID, target pod)` to gate per-pod `Apply` triggers.   |
| `time.Sleep`          | `func(d time.Duration)`                                   | stdlib                  | `mongo-pause-500ms.Apply` (the 500ms window between Pause and Unpause).          |
| `sync.WaitGroup`      | `Add`/`Done`/`Wait`                                       | stdlib                  | `concurrent-fan-out-3.Apply` (join the 3 actor goroutines).                      |

That's the entire surface. Three project interfaces (DockerCLI,
Dispatcher, Observer) plus two stdlib primitives.

**Each Executor's `Apply` and `Cleanup` is a few lines composing
these.** No hidden machinery, no per-scenario plumbing:

```go
// crash
func (e *crashExecutor) Apply(ctx context.Context, trig <-chan struct{}) error {
    <-trig                                          // wait for in-scope event
    return e.docker.Restart(ctx, e.container)       // single primitive call
}
func (e *crashExecutor) Cleanup(ctx context.Context) error {
    return e.docker.StartIfStopped(ctx, e.container)
}

// mongo-pause-500ms
func (e *mongoPauseExecutor) Apply(ctx context.Context, trig <-chan struct{}) error {
    <-trig
    if err := e.docker.Pause(ctx, "mongo"); err != nil { return err }
    time.Sleep(500 * time.Millisecond)
    return e.docker.Unpause(ctx, "mongo")
}
func (e *mongoPauseExecutor) Cleanup(ctx context.Context) error {
    return e.docker.UnpauseIfPaused(ctx, "mongo")
}

// concurrent-fan-out-3
func (e *concurrentFanOutExecutor) Apply(ctx context.Context, _ <-chan struct{}) error {
    var wg sync.WaitGroup
    for _, actor := range e.actors {              // 3 actors from cast
        wg.Add(1)
        go func(a Actor) {
            defer wg.Done()
            e.dispatcher.FireWithActor(ctx, e.scenario, a)
        }(actor)
    }
    wg.Wait()
    return nil
}
func (e *concurrentFanOutExecutor) Cleanup(ctx context.Context) error {
    return nil                                     // no persistent state to undo
}
```

That's it. Reading any Executor's source is reading ~5–10 lines of
plumbing over the toolkit. No reflection, no codegen, no string
dispatch.

**What scales as scenarios grow.** Only data:

- New rows in `catalogs/services/*.yaml` (a service's `mishap_eligible`,
  `container`, `uses_mongo` flags — see §4.2).
- New scenario YAMLs (pure data — verbs, expected reads, etc.).
- New cast fixtures.

**What scales as mishap kinds grow.** A small, code-level extension:

- One new file in `internal/mishap/` implementing the Executor
  interface (§3.1).
- One registry entry mapping kind name → factory.
- Possibly one new method on DockerCLI (e.g. a future `KillSIGSTOP`
  for a different fault model). This extends the toolkit deliberately
  and visibly; reviewers can audit the entire toolkit by reading
  three small interfaces.

**Why this matters.** The toolkit is what we audit for correctness
and safety once — docker calls are idempotent, dispatcher is safe to
call concurrently, observer triggers are one-shot per
`(traceID, ownerPod)`. Once that audit passes, no future scenario
can violate it because no future scenario can reach below this layer.
The blast radius of "add a scenario" is bounded to data files.

---

## 4. Catalog YAML and runner integration

### 4.1 YAML schema

Mishap kinds live under `catalogs/mishaps/<name>.yaml`. The schema
is intentionally narrow — three files, one per kind.

```yaml
# catalogs/mishaps/crash.yaml
mishap: crash
kind: per-pod
class: idempotency
factory: CrashFactory
eligible: all-pods                         # named PodPredicate
notes: |
  docker restart of the target pod, triggered when the first in-trace
  event owned by that pod arrives at the observer. Fans out across
  every pod in scenario.sequence (one case per pod).
```

```yaml
# catalogs/mishaps/mongo-pause-500ms.yaml
mishap: mongo-pause-500ms
kind: per-pod
class: resilience
factory: MongoPauseFactory
eligible: mongo-using-pods                 # named PodPredicate
parameters:
  pause_container: mongodb                 # what gets paused (always literal)
  pause_ms: 500
notes: |
  When the first in-trace event owned by a Mongo-using pod arrives,
  pause the mongodb container for 500ms. Fans out across every
  Mongo-using pod in scenario.sequence.
```

```yaml
# catalogs/mishaps/concurrent-fan-out-3.yaml
mishap: concurrent-fan-out-3
kind: sequence-level
class: concurrency
factory: ConcurrentFanOut3Factory
parameters:
  n: 3
notes: |
  Fires the scenario's verb 3 times in parallel from 3 distinct cast
  fixtures matching the scenario's placeholder predicate. Per-pod
  mishaps composed with this kind fire ONCE PER ACTOR (per trace) —
  three perturbations per case, one per actor's first arrival at
  the target. Retries within any single actor's trace do not
  re-fire that actor's perturbation (misfortune, not adversary —
  see §2.2).
```

The `factory` and `eligible` fields MUST resolve to Go symbols
registered in `internal/mishap/registry.go` at startup. The
`validator` CLI (Part-1) is extended to cross-check both — drift
surfaces immediately the same way a renamed verb factory would.

Two named predicates ship with Part-2:

```go
// internal/mishap/predicates.go
RegisterPodPredicate("all-pods", func(p ServiceProfile) bool {
    return p.MishapEligible  // defaults to true; opt out per service
})
RegisterPodPredicate("mongo-using-pods", func(p ServiceProfile) bool {
    return p.MishapEligible && p.UsesMongo()  // UsesMongo: any trigger writes/reads mongo.*
})
```

### 4.2 Service ability profile additions

Two fields are added to every `catalogs/services/<name>.yaml`:

```yaml
service: room-worker
container: room-worker              # docker-compose container name
mishap_eligible: true               # defaults to true; set false to opt out
triggers:
  - source: jetstream-pull
    pattern: "chat.room.canonical.*.create"
    on_trigger:
      - writes: mongo.rooms          # ← detected as "uses mongo"
      - writes: logs.room-worker
```

The `UsesMongo()` predicate scans `triggers[*].on_trigger[*].writes`
and `…reads` (when read-effects land in Part-2) for any entry
starting with `mongo.`. No new field is needed for that — the
ability profile already encodes it.

`mishap_eligible: false` opts a service out of being targeted by any
per-pod mishap. `auth-service` is the obvious example (the suite
authenticates through it; restarting it mid-case would break the
suite's own auth path, not the system).

### 4.3 Case expansion — nested loop, not power set

The power-set expander (`internal/mishap/expander.go`) is replaced
by a **3-axis nested loop** derived from the scenario's sequence and
ability profiles:

```python
def expand_cases(scenario, services):
    pods_in_seq = unique([readers[s.at].owner for s in scenario.sequence])
    pods_in_seq = [p for p in pods_in_seq if services[p].mishap_eligible]
    mongo_pods  = [p for p in pods_in_seq if services[p].uses_mongo()]

    # None FIRST in each axis: the loop's natural enumeration order
    # then becomes happy → single-axis crash → single-axis mongo →
    # combined, which matches the phased execution order (§4.3.1)
    # and the report row order (§5.1). Aligning these three is what
    # lets the skip rules read top-to-bottom without re-sorting.
    crash_targets = [None] + pods_in_seq   # None == "no crash this case"
    mongo_targets = [None] + mongo_pods
    concurrent    = [False, True]          # c=0 before c=1 (skip-rule gating)

    for c in concurrent:
        if c and not scenario.has_concurrent_eligible_cast(n=3):
            continue                       # preflight skip
        for m in mongo_targets:
            for x in crash_targets:
                yield Case(scenario, concurrent=c, mongo=m, crash=x)
```

The loop's nesting order — `c → m → x` with None first in each
inner axis — is **fixed** and **deterministic**. It defines:

- The set of cases generated for a scenario.
- The row order in the report (§5.1).
- The natural reading order: happy first, single-axis perturbations
  next, combined last, c=0 before c=1.

**Loop order ≠ execution order, but they align.** Execution follows
the phased order in §4.3.1 (happy → single-axis c=0 → combined c=0
→ all c=1) so that skip-rule verdicts from earlier phases can gate
later phases. Because we put None first in each axis, the loop's
natural enumeration *also* produces happy → single-axis → combined
within each `c` level — so a runner that walks the loop output
linearly within each phase produces the right execution order with
no separate sort. The two orderings agree on simpler-first; the only
divergence is that c=0 must fully complete before c=1 begins, which
the outer `for c` loop already arranges.

### 4.3.1 Skip rules and phased run order

Cases run in **phases**; each phase's verdicts gate the next. Cases
the rules eliminate are **skipped, not removed** — they appear in
the report under their stable IDs (§4.4) with `skipped (reason)`
in the latest column, full grid intact.

```
phase 0:  happy             [c=0 m=- x=-]
phase 1:  single-axis c=0   [c=0 m=- x=POD] for each POD
                            [c=0 m=POD x=-] for each POD
phase 2:  combined c=0      [c=0 m=M x=X] where both ≠ None
phase 3:  c=1               every case with c=1
```

The skip rules, evaluated in order — first match wins, single reason
recorded:

```
rule H — happy short-circuit:
    if  verdict(scenario[c=0 m=- x=-]) is not pass*
    then skip every other case in the scenario
         reason: "happy failed"

rule X — crash-axis propagation:
    for each POD where verdict(scenario[c=0 m=- x=POD]) is not pass*:
        skip every case with x=POD (any m, any c)
        reason: "x=POD fails alone"

rule M — mongo-axis propagation:
    for each POD where verdict(scenario[c=0 m=POD x=-]) is not pass*:
        skip every case with m=POD (any x, any c)
        reason: "m=POD fails alone"

rule P — pairwise c=1 (final mop-up):
    for each (M, X) where verdict(scenario[c=0 m=M x=X]) is not pass*:
        skip scenario[c=1 m=M x=X]
        reason: "c=0 partner failed"

* "not pass" = anything other than `pass` or `pass-with-relaxation`.
  The relaxation downgrade (§4.6) is auditable but the system did
  absorb the perturbation, so it gates nothing.
```

**Rationale.**

- **Rule H.** A happy fail means the system is broken at baseline.
  Mishap results layered on a broken baseline are noise — perturbation
  failures get attributed to whatever's wrong with happy, not to the
  perturbation. Skipping the whole grid (and reporting all rows as
  `skipped (happy failed)`) gives a single, unambiguous signal:
  "fix happy first."

- **Rules X and M.** If a single-axis perturbation fails on its own,
  the defect lives in that axis, not in axis composition. Running
  `[c=0 m=POD x=room-worker]` when `[c=0 m=- x=room-worker]` already
  fails would just re-find the same crash defect with extra
  variables; running `[c=1 m=- x=room-worker]` would compound it
  across actors. Both get attributed to the single-axis defect.

- **Rule P.** Already covered (§prior revision) — concurrency on
  top of a working `(m, x)` config is the actual property under
  test; concurrency on top of a broken one tests nothing new.

**Pairwise example.** If `[c=0 m=- x=room-worker]` fails alone, Rule
X skips: `[c=0 m=room-worker x=room-worker]`, every `[c=1 * x=room-
worker]`, etc. — five cases in the room-create-001 grid, each rowed
in the report as `skipped (x=room-worker fails alone)`. The reviewer
sees the propagation explicitly and can fix the root cause without
chasing downstream noise.

### 4.3.2 Case enumeration (combinations that make sense)

Each case is uniquely identified by its loop indices. **No
composition-filter table needed.** The structure naturally produces
exactly the combinations that make sense:

- `c=0 m=None x=None` — the happy case.
- `c=0 m=None x=pod` — single-request, pod crash on arrival.
- `c=0 m=pod x=None` — single-request, mongo paused at pod.
- `c=0 m=pod x=pod` — single-request, pod crashes AND mongo paused.
  Same-pod-for-both is allowed (e.g. both = room-worker). It tests a
  real cascading-failure scenario (mongo flaky + pod restarts during
  retry); the two perturbations probe orthogonal properties.
- `c=1 …` — same matrix, with 3 actors. Per-pod mishaps fire ONCE
  PER ACTOR (one trigger per actor's trace, fired on that actor's
  first arrival at the target). Three actors → three perturbations
  per case. Each actor's retries within its own trace don't re-fire
  its own perturbation. See §2.2.

### 4.3.3 Ignore semantics — history-aware opt-out

`scenario.mishaps.ignore` opts a scenario out of particular mishap
kinds. It is **history-aware**, not a hard pre-filter:

```yaml
mishaps:
  ignore:
    - kind: crash             # don't run, preserve history rows
    - kind: mongo-pause-500ms
      drop: true              # don't run AND drop history rows
```

Shorthand — a bare string is `{kind: <string>, drop: false}`:

```yaml
mishaps:
  ignore: [crash]             # same as `- kind: crash`
```

Resolution (per generated case, before the case is queued for
execution):

```
for each case in expand_cases(scenario, services):
    ignored_kinds = the case's non-trivial-axis kinds that appear
                    in scenario.mishaps.ignore
    
    if ignored_kinds is empty:
        queue the case for normal execution
        continue
    
    # case touches at least one ignored kind
    has_history = run-history file has prior entries for this case ID
    drop_requested = any matching ignore entry has drop: true
    
    if has_history and drop_requested:
        delete history for this case ID
        OMIT row from this run's report      # destructive opt-in
    elif has_history:
        INCLUDE row, latest = `skipped (ignored: <kinds>)`,
                     best/worst preserved from history
    else:
        OMIT row entirely                    # never "create just to skip"
```

The intent is three behaviors:

1. **First-time ignore — rows just don't exist.** A user who sets
   `ignore: [crash]` from a scenario's first run never sees any
   `x≠None` rows in the report. The grid the user sees is the grid
   they care about. No clutter.

2. **Toggling ignore on a working scenario preserves history.** A
   user who ran the full grid, then added `ignore: [crash]`, sees
   the same row set on the next run, but every `x≠None` row shows
   `latest = skipped (ignored: crash)` with best/worst still
   reflecting prior real runs. History is data the user worked to
   produce; we don't silently discard it.

3. **`drop: true` is the destructive opt-in.** Removes both the
   next run's rows AND prior history. Use when you've decided a
   kind is no longer interesting and want to retire those rows
   permanently. Idempotent — after one run with `drop: true`, the
   rows are gone and subsequent runs see no-history → omit.

**Skip-rule interaction.** Ignored cases never reach the phase
runner. Their `skipped` verdict does NOT propagate via Rule
X/M/P — those rules require an actual `fail`, not a `skipped`. An
ignored single-axis case neither propagates nor gates anything.

**Coverage interaction.** Ignored cases don't move coverage (same
as any skipped case — coverage tracks happy passes per §5.3).

**Multi-kind compound cases.** A combined case like
`[c=0 m=room-worker x=room-service]` touches both kinds. If both
are ignored:

- `skipped (ignored: crash + mongo-pause-500ms)` — both kinds named
  in the reason, joined by `+`. The reviewer sees the full set of
  ignore entries that match this row, no first-match guessing.
- If only one is ignored, only that one is named.

**`concurrent-fan-out-3` is ignorable too.** `ignore: [concurrent-
fan-out-3]` suppresses every c=1 case in the scenario under the
same history-aware rules. Useful for scenarios whose verb genuinely
cannot fan out (e.g. a global-singleton operation).

### 4.4 Case naming

Each case has a deterministic name derived from the loop indices:

```
<scenario.id>[c=<0|1> m=<pod|-> x=<pod|->]

examples:
  room-create-001[c=0 m=- x=-]                  ← happy
  room-create-001[c=0 m=- x=room-worker]        ← crash room-worker
  room-create-001[c=0 m=room-worker x=-]        ← mongo paused at room-worker
  room-create-001[c=0 m=room-worker x=room-worker]   ← both, same pod
  room-create-001[c=1 m=- x=room-service]       ← 3 actors, room-service crashed once per actor
  room-create-001[c=1 m=room-worker x=room-worker]   ← 3 actors, both at room-worker
```

The name is the case's identity everywhere — reporter rows, log
lines, blindspot citations. No allocator state needed; pure function
of (scenario, concurrent, mongo_target, crash_target).

### 4.5 Case-budget math

For the size-2 `room-create-001` example:

```
pods_in_seq  = [room-service, room-worker]
mongo_pods   = [room-worker]                 # room-service publishes events; doesn't write Mongo

cases = 2 (concurrent) × 2 (mongo+None) × 3 (crash+None) = 12
```

Generalizing for a scenario with `p` pods in sequence and `m` of
them Mongo-using, **`cases_attempted` distinguishes from
`cases_reported`** — the §4.3.1 skip rules suppress *execution*, not
reporting. The full grid is always rowed:

```
cases_reported  = 2 × (m + 1) × (p + 1)         (always — every cell)

cases_attempted_max = 2 × (m + 1) × (p + 1)     (all c=0 pass)
cases_attempted_min = 1                         (happy fails → rule H
                                                 short-circuits all)

intermediate min when happy passes but one axis fails entirely:
    rule X kills all cases with x=POD_x  → (m+1) cases survive ×2 c
    rule M kills all cases with m=POD_m  → (p+1) cases survive ×2 c
```

| Sequence shape          | p | m | reported | max run | min run (happy fail) |
|-------------------------|---|---|----------|---------|----------------------|
| Size-2, 1 mongo (room)  | 2 | 1 | 12       | 12      | 1                    |
| Size-3, 2 mongo (msg)   | 3 | 2 | 24       | 24      | 1                    |
| Size-4, 3 mongo (fed)   | 4 | 3 | 40       | 40      | 1                    |
| Size-5, 3 mongo (chain) | 5 | 3 | 48       | 48      | 1                    |

In practice runs land close to max — c=0 cases pass on a healthy
system. Skips are a wall-clock optimization for broken-system runs:
the report keeps its full shape so historical tracking (§5.1) stays
intact across runs of different health, while attempts drop sharply
when a regression breaks an axis.

**Wall-clock estimate.** Durations are *observed-typical*, not caps —
mishap cases close on quiet-detection alone (§4.6), so a hung
service hangs the case rather than failing at a hard cap.

| Case shape (per scenario)              | Observed-typical duration |
|----------------------------------------|---------------------------|
| `c=0 m=- x=-` (happy)                  | ~250ms                    |
| `c=0 m=pod x=-`                        | ~750ms                    |
| `c=0 m=- x=pod`                        | ~3.5s                     |
| `c=0 m=pod x=pod`                      | ~4s                       |
| `c=1 m=- x=-`                          | ~400ms                    |
| `c=1 m=pod x=-`                        | ~1s                       |
| `c=1 m=- x=pod`                        | ~3.8s                     |
| `c=1 m=pod x=pod`                      | ~4.2s                     |

For a 6-scenario Part-2 with average ~15 cases/scenario at ~1.5s
typical, **~135s per run on a healthy system.** Bounded; fits CI;
fits a developer's local feedback loop.

**Retry-on-fail cost** (§4.6.1). Failing cases run up to 3 attempts
(1 initial + 2 retries). On a healthy system this is zero overhead
(no cases fail). On a broken system: the skip rules (§4.3.1)
collapse a single-axis regression to ~5 *attempted* cases per
affected scenario; each costs 3×, giving ~15 attempts. The worst
case is "happy fails" — exactly 3 attempts of one case, then the
entire scenario short-circuits.

If scenario count grows to ~20 in Part-2.5, the typical run scales
linearly to ~7 minutes. The lever to bound case count is the
`scenario.mishaps.ignore` list (§4.3.3) — it omits matching cases
from execution. Whether the omitted cases also disappear from the
report depends on history: first-time-ignored cases are never
created, prior-history cases stay rowed with `skipped (ignored:
<kind>)` unless `drop: true` is set.

### 4.6 Verdict semantics under a mishap-applied case

**Timeframe under a mishap is event-driven only.** The scenario's
`timeframe:` declaration (the per-scenario duration cap used in the
happy case) is **discarded** for any case where any axis is non-trivial
(`c=1`, `m≠None`, or `x≠None`). Closure is the quiet-detection rule
from the parent design applied with no upper bound:

```
T_open  = observer.Start(...)              ── before the verb fires
fire verb (+ apply mishaps per §3.2 — triggered on per-pod arrival)
loop:
  on event in-scope (trace match AND owner in scenario.Sequence services):
      lastInScopeAt = ev.Timestamp
  on 50ms tick:
      if has-seen-events AND time.Since(lastInScopeAt) >= quietGrace:
          T_close = now ── done
  (NO safety cap — the case runs until the cascade goes quiet)
```

The `quietGrace` window is bumped from 200ms (happy default) to
**1s under any non-empty mishap configuration** to absorb
post-perturbation jitter.

Rationale: mishap is a **survival test**, not a performance one. The
scenario author commits to a timeframe under the happy case because
they know the steady-state cascade duration. They cannot honestly
commit to one under a restart, a pause, or a concurrent fan-out —
the system's reaction time under perturbation is the property the
system promises *not* to fail, not a number we can write down in
advance. A hard cap would just turn slow-but-correct recoveries into
spurious failures and hide the real signal.

The trade-off: a genuinely deadlocked service hangs the case until
the outer Go test timeout fires. That's acceptable — a hung service
is a real defect the developer needs to see, not a soft
"timeframe-never-closed" verdict the eye skips over.

Per-axis classification rules:

```
HAPPY (c=0 m=None x=None):
    standard 3-way classification from parent design.
    scenario.timeframe applies (hard close at that duration).
    pass iff: every required read matched, no unexpected cascade,
              no anomaly.

ANY non-empty axis:
    NO timeframe cap from the scenario.
    quietGrace = 1s.
    expected reads MUST still all match.

x ≠ None  (crash applied to target pod X):
    "anomaly" entries whose owner is X, emitted within ±100ms of the
    perturbation window, are downgraded to "background" — they're
    recovery-path log noise, not real anomalies. The reporter logs
    the downgrade count per case; if a downgrade is the difference
    between pass and fail, the case is marked "passed-with-relaxation"
    not plain "pass".

m ≠ None  (mongo paused triggered at pod M):
    no anomaly relaxation — mongo isn't an event source in the
    cascade, only a destination. Pod M's writes will retry; that's
    the property under test.

c = 1  (concurrent fan-out):
    per-actor classification — three traceIDs, three independent
    cascades. The case passes iff EVERY actor's reads close cleanly.
    (Stricter than the parent v2 design's "one or any" — for Part-2
    we want the all-pass signal because that's what concurrency
    correctness means for our domain.)
    When combined with a per-pod mishap, EACH actor gets its own
    perturbation, fired once on that actor's first arrival at the
    target — three perturbations per case, one per trace. Retries
    within any actor's trace do not re-fire that actor's
    perturbation. See §2.2.
```

**No fail-fast.** A case keeps running after the first actor or read
fails — every actor's cascade is observed to completion, every
expected read is checked, every perturbation completes its lifecycle.
The verdict lists all failures in **occurrence order**, by timestamp,
so a reviewer can read the cascade chronologically. Cutting off at
the first failure would hide downstream issues (e.g. "actor B failed
because the room-worker restart left a dangling subscription state
that also broke actor C's read"). The runner's per-case loop
collects every failure event and emits them ordered in the report
(§5).

A case that fails under any non-trivial axis but passes happy is the
**signal of interest** — a real defect that only shows under
perturbation. The reporter highlights these explicitly (§5).

### 4.6.1 Retry-on-fail (noise reduction)

Integration suites are exposed to a long tail of incidental flakes —
a transient docker daemon hiccup, a kind kube-proxy reconvergence, a
testcontainer cleanup straggler from an earlier run, an NATS
reconnect window after a previous case's mishap, etc. None of those
are defects in the system under test; they're noise in the harness
or its environment. To suppress them, **a failing case runs at most
3 times total** — 1 initial attempt plus 2 retries — before its
verdict is recorded.

```
attempt 1: initial run         → pass  → record `pass`, move on
                               → fail  → keep error block, retry
attempt 2: retry 1             → pass  → record `pass`, move on
                               → fail  → keep error block, retry
attempt 3: retry 2             → pass  → record `pass`, move on
                               → fail  → record `fail`, emit ALL
                                         THREE error blocks
```

**One pass anywhere is a pass.** As soon as any attempt passes, the
case's verdict is `pass` (or `pass-with-relaxation` if that attempt
relaxed) and the runner moves on. Earlier failed attempts are
**not** recorded in the report — they were noise, by hypothesis,
since a successive attempt under identical configuration passed.

**All-fail emits all three error blocks verbatim.** No deduplication,
no comparison, no "errors differed between attempts" annotation —
just `attempt 1: <errors>`, `attempt 2: <errors>`, `attempt 3:
<errors>` rendered back-to-back under the case's row. Redundancy is
fine; the reviewer reads three identical blocks and concludes
"consistent failure, real defect," or reads three different blocks
and concludes "intermittent, dig deeper." Either is useful; the
runner doesn't pre-judge.

**Per-attempt isolation.** Each attempt is a full lifecycle from
§3.1 — Build → trigger channel allocation → Apply → cascade observe
→ Cleanup. Mishaps fire fresh on each attempt (new traceID under
c=0, three new traceIDs under c=1), and Cleanup runs between
attempts so the system starts each retry from a known state. No
attempt observes another attempt's events.

**What retries.** Only `fail` triggers retry. `skipped` doesn't
(nothing ran). `pass-with-relaxation` doesn't (it's a pass for skip
purposes — the relaxation is auditable but the system did handle
the perturbation).

**Skip-rule interaction.** Skip rules (§4.3.1) consult the *final*
recorded verdict, after retries. A c=0 case that fails attempt 1 but
passes attempt 2 records `pass` and does NOT propagate skips. A c=0
case that fails all three attempts records `fail` and triggers the
appropriate Rule H/X/M/P propagation.

**Wall-clock impact.** A failing case costs 3× a passing case's
duration. Healthy systems pay nothing for retries (cases pass first
attempt). Broken systems pay 3× on the cases that actually fail —
but the skip rules limit how many cases reach the retry loop in the
first place, so a single-axis regression (~5 cases failing in a
size-2 scenario) costs roughly 5×3 = 15 attempts rather than the
naive 36 (12 cases × 3 attempts).

### 4.7 Runner integration points

Files in `internal/runtime/` that need changes:

| File                    | Change                                                                                                                                                                                                                  |
|-------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `runner.go`             | For each scenario, call `mishap.ExpandCases(scenario, services)` to get the full case list. Execute in §4.3.1 phase order: happy → single-axis c=0 → combined c=0 → all c=1. After each phase, evaluate skip rules (H → X → M → P, first match wins) against the verdicts seen so far. Cases the rules eliminate are NOT executed but ARE recorded with verdict `skipped` and the matched-rule reason. Every case in the expanded list gets a row in the report regardless of whether it ran. |
| `runOneCase`            | **Runs the case up to 3 times on fail (§4.6.1) — 1 initial + 2 retries.** For each attempt: builds an Executor per non-trivial axis via the registry; allocates one trigger channel per `(traceID, target pod)` tuple — 1 channel for c=0, 3 channels for c=1 per per-pod Executor; wires `observer` to send-and-close on each channel's first matching event; subsequent matching events on the SAME trace flow to classification but do NOT re-fire (§2.2); spawns one `Apply` goroutine per channel; defers `Cleanup` for each. Does NOT fail-fast within an attempt — collects all failures across actors/traces in occurrence order. If the attempt passes (or pass-with-relaxation), records the verdict and returns immediately. If it fails, captures the attempt's error block, runs Cleanup, and retries with fresh traceIDs and fresh channels — up to 3 attempts total. Final fail emits all 3 error blocks verbatim. |
| `observer.go`           | Adds `OnFirstInScope(traceID string, ownerPod string, ch chan<- struct{})` registration: fires on the first event whose owner == ownerPod AND traceID matches, then deregisters. One-shot per `(traceID, ownerPod)` tuple per §2.2. The runner makes one registration per trace under c=1 (three registrations, three channels). Existing classification path unchanged. |
| `dispatcher.go`         | Adds `FireWithActor(ctx, scenario, actor)` so `concurrentFanOut.Apply` can drive per-actor fires. Existing `Fire` signature unchanged for c=0 cases.                                                                     |
| `verdict.go`            | Reads `Case.concurrent`, `Case.mongoTarget`, `Case.crashTarget`; applies the §4.6 rules per-axis. Discards `scenario.timeframe` when any axis is non-trivial.                                                            |
| `reporter.go`           | Per-case row uses the §4.4 name (`[c=0 m=- x=room-worker]`). Adds the "pass under happy / fail under perturbation" highlight section.                                                                                    |

`DockerCLI` is a small wrapper over `exec.Command("docker", …)`. It
lives in `internal/mishap/docker.go` and is the only place in the
suite that shells out to docker. The interface is mockable for unit
tests; integration runs use the real docker CLI on the host.

```go
type DockerCLI interface {
    Pause(ctx context.Context, container string) error
    Unpause(ctx context.Context, container string) error
    Restart(ctx context.Context, container string) error
    UnpauseIfPaused(ctx context.Context, container string) error
    StartIfStopped(ctx context.Context, container string) error
}
```

Preflight (already in the Makefile) gets one new check: every
container named in `catalogs/services/*.yaml`'s `container:` field
must be present in `docker ps`. Mishap runs fail fast otherwise.

---

## 5. Reporting

`docs/integration-suite-v2/last-run.md` gets a new case naming
convention and a new highlight section.

### 5.1 Per-case rows — stable IDs, latest/best/worst tracking

The report's per-case rows are a view of `performance.json` (§5.4).
The case name (§4.4) is the row identity. Because case expansion is
**deterministic** (a pure function of scenario + concurrent + mongo
target + crash target), the same row appears on every rerun. The
report tracks three verdicts per row:

- **latest** — the verdict from the current run. Includes `skipped`
  with a granular reason.
- **best** — the most favorable verdict observed across all prior
  *executed* runs of that case. `skipped` runs are NOT eligible for
  best (the case didn't run, so it can't qualify as a best run).
- **worst** — the least favorable verdict observed across all prior
  *executed* runs. Same exclusion: `skipped` doesn't pollute worst.

This means a case can show `latest=skipped (x=room-worker fails
alone)`, `best=pass`, `worst=fail (actor #2 missing)` — the latest
column tells you what happened *this* run; best/worst tell you the
range of real outcomes when this case *did* run.

Sample run: happy passes, `[c=0 m=- x=room-worker]` fails (a worker
regression), everything else healthy. Rule X fires, propagating
skips across the `x=room-worker` column.

```
| case                                             | latest                              | best | worst | reads | cascades | duration |
|--------------------------------------------------|-------------------------------------|------|-------|-------|----------|----------|
| room-create-001[c=0 m=- x=-]                     | pass                                | pass | pass  | 2/2   | 1        | 240ms    |
| room-create-001[c=0 m=- x=room-service]          | pass                                | pass | pass  | 2/2   | 1        | 3.4s     |
| room-create-001[c=0 m=- x=room-worker]           | fail (worker hung after restart)    | pass | fail  | 1/2   | 1        | 12.1s    |
| room-create-001[c=0 m=room-worker x=-]           | pass-with-relaxation (2↓)           | pass | p-w-r | 2/2   | 1        | 3.4s     |
| room-create-001[c=0 m=room-worker x=room-service]| pass                                | pass | pass  | 2/2   | 1        | 3.9s     |
| room-create-001[c=0 m=room-worker x=room-worker] | skipped (x=room-worker fails alone) | pass | pass  | —     | —        | —        |
| room-create-001[c=1 m=- x=-]                     | pass                                | pass | fail  | 2/2×3 | 3        | 410ms    |
| room-create-001[c=1 m=- x=room-service]          | pass                                | pass | pass  | 2/2×3 | 3        | 4.0s     |
| room-create-001[c=1 m=- x=room-worker]           | skipped (x=room-worker fails alone) | pass | pass  | —     | —        | —        |
| room-create-001[c=1 m=room-worker x=-]           | pass                                | pass | pass  | 2/2×3 | 3        | 4.2s     |
| room-create-001[c=1 m=room-worker x=room-service]| pass                                | pass | pass  | 2/2×3 | 3        | 4.2s     |
| room-create-001[c=1 m=room-worker x=room-worker] | skipped (x=room-worker fails alone) | pass | pass  | —     | —        | —        |
```

Three rows show `skipped (x=room-worker fails alone)` — Rule X
propagated the row 3 failure across every case with `x=room-worker`.
Note `[c=1 m=room-worker x=-]` (row 10) is NOT skipped: its c=0
partner row 4 is `pass-with-relaxation` which counts as pass for
Rule P. Best/worst on the skipped rows reflect prior actual runs and
stay frozen — the `pass` values came from earlier executions when
the worker was healthy.

Skip reasons fall into two groups — config-driven (§4.3.3) and
runtime (§4.3.1):

- `skipped (ignored: <kind>)`            — §4.3.3, scenario.mishaps.ignore matched
- `skipped (ignored: <kind-A> + <kind-B>)` — §4.3.3, multi-kind compound case
- `skipped (happy failed)`               — rule H
- `skipped (x=POD fails alone)`          — rule X
- `skipped (m=POD fails alone)`          — rule M
- `skipped (c=0 partner failed)`         — rule P

Ignore is resolved before any phase runs, so an ignored row never
reaches the phase rules and never gets a runtime skip reason. Among
runtime rules, first-match wins; only one reason is recorded per row.

The `(2↓)` annotation indicates 2 anomalies were downgraded to
background under the §4.6 rule. This keeps the relaxation auditable
— a reviewer can spot a relaxation that's masking real noise.

When a case fails, an indented sub-section follows the row. Two
levels of detail:

**Within a single attempt** — every failure in **occurrence order**
(timestamped), not just the first one. This is the no-fail-fast
policy from §4.6.

**Across attempts** — all 3 attempts' error blocks rendered
back-to-back when retry-on-fail (§4.6.1) exhausted. No
deduplication, no comparison.

```
| room-create-001[c=1 m=- x=room-worker]      | fail | 2/2 ×1, 1/2 ×2 | 3 | 12.3s

  attempt 1 (initial):
    +0.42s  actor #2  trace=ab12  missing read mongo.rooms
    +0.51s  actor #3  trace=cd34  unexpected cascade: subscription.created with stale roomID
    +1.20s  actor #1  trace=ef56  pass (after retry)

  attempt 2 (retry 1):
    +0.39s  actor #2  trace=gh78  missing read mongo.rooms
    +0.48s  actor #3  trace=ij90  unexpected cascade: subscription.created with stale roomID
    +1.18s  actor #1  trace=kl12  pass (after retry)

  attempt 3 (retry 2):
    +0.41s  actor #2  trace=mn34  missing read mongo.rooms
    +0.50s  actor #3  trace=op56  unexpected cascade: subscription.created with stale roomID
    +1.19s  actor #1  trace=qr78  pass (after retry)
```

Reading top-to-bottom within an attempt gives the cascade
chronologically: actor #2's failure first; actor #3's unexpected
event with a stale ID (state-leak between cascades); actor #1
recovered.

Reading across attempts: three nearly-identical blocks confirm this
is a **real defect**, not a flake — the system reproducibly fails
when room-worker is crashed under concurrent fan-out. Three
*different* blocks would suggest intermittent behavior; either
outcome is useful, and the runner doesn't attempt to summarize.
Note duration is the wall-clock sum of all 3 attempts (12.3s ≈
4.1s × 3); `reads matched` and `cascades` columns show **the
final attempt's** numbers — the report doesn't try to merge them.

If any attempt passes, only that pass is recorded — earlier
attempt blocks are discarded as noise (§4.6.1).

The `2/2 ×3` notation in c=1 rows means "all 3 actors closed 2/2
reads." `2/2 ×2, 1/2 ×1` means two actors closed all reads, one
actor missed one — the case fails because c=1 is all-pass.

### 5.2 "Perturbation defects" highlight section

A new top-of-report section lists every scenario where the happy
case passed but some perturbation-applied case failed:

```
happy(scenario) == pass  AND  any non-trivial-axis case(scenario) == fail
```

This is the signal of interest for a reviewer. Example:

```markdown
## Perturbation defects (passed happy, failed under perturbation)

- **room-create-001[c=1 m=- x=-]** — actor #2's cascade missed
  `mongo.rooms` write. Likely race on room-ID generation
  (see [room-worker handler.go:178]).
- **message-send-003[c=0 m=- x=broadcast-worker]** — broadcast
  cascade never produced fan-out events after restart. JetStream
  durable-consumer state not recovered.
```

Skipped cases (§4.3.1) are excluded from this section — they're
neither pass nor fail, so they cannot signal a perturbation defect.
The root-cause case that triggered the skip (the single-axis fail
that fired Rule X or M) IS listed; the propagated skips that
followed are not. That's the right level of detail for a reviewer:
one defect, one entry.

The report generator builds this section from the case grid; no
extra annotation needed in scenario YAML.

### 5.3 Coverage interaction

The Part-1 coverage doc (`docs/integration-suite-v2/coverage.md`) is
**unaffected by mishaps**. A scenario claims a coverage case via
`@covers:` tags; the case counts as covered when the **happy** case
passes (`c=0 m=- x=-`). Perturbation failures are reported separately
(§5.2) and do not move the coverage needle.

This split matters: coverage measures "did we test this behavior";
perturbation defects measure "does the system survive perturbation
of behavior we test." Mixing them would conflate two different
things.

### 5.4 `performance.json` — source of truth

The report is **rendered from** a persistent JSON data file. The
file is the source of truth; `last-run.md` is a view. This is what
makes latest/best/worst (§5.1), history-aware ignore (§4.3.3), and
cross-run row stability work.

**Location.** `docs/integration-suite-v2/performance.json`.

**Commit policy.** Committed. History across machines and CI runs is
only meaningful if it accumulates in one place; a gitignored file
would reset on every fresh checkout and defeat the purpose. CI runs
on `main` commit updates back the same way they do for any other
generated doc; PR runs render a local view but don't push.

**Schema.**

```json
{
  "schemaVersion": 1,
  "lastRunAt": "2026-05-25T14:32:11Z",
  "cases": {
    "<case-id>": {
      "latest": {
        "ranAt":         "2026-05-25T14:32:11Z",
        "verdict":       "pass" | "pass-with-relaxation" | "fail" | "skipped",
        "skipReason":    "<rule reason>",        // only when verdict == "skipped"
        "readsMatched":  "2/2" | "2/2 ×3" | …,    // omitted when skipped
        "cascades":      1 | 3 | …,                // omitted when skipped
        "durationMs":    240,                      // sum across retry attempts
        "errorBlocks": [                           // present only when verdict == "fail"
          { "attempt": 1, "label": "initial", "events": [ … per-event records … ] },
          { "attempt": 2, "label": "retry 1", "events": [ … ] },
          { "attempt": 3, "label": "retry 2", "events": [ … ] }
        ],
        "relaxationCount": 2                       // only when verdict == "pass-with-relaxation"
      },
      "best":  { "verdict": "pass",                "ranAt": "2026-05-20T09:11:02Z" },
      "worst": { "verdict": "fail",                "ranAt": "2026-04-30T13:02:55Z" }
    }
  }
}
```

Notes on fields:

- **`latest`** always reflects the current run, including `skipped`.
- **`best`** and **`worst`** only update on **executed** runs — never
  on `skipped`. A skipped case leaves best/worst untouched.
- **`errorBlocks`** capture the no-fail-fast (§4.6) per-attempt
  failure events when retry-on-fail (§4.6.1) exhausted. Their shape
  is the per-event records the runner already collects; not
  prescribed here in detail.
- **`durationMs`** is the wall-clock sum across all attempts in the
  case (so a 3-attempt fail has a ~3× the single-attempt duration).

**Verdict ordering.** Used by best/worst comparisons:

```
favorable → unfavorable
   pass    >    pass-with-relaxation    >    fail
```

`skipped` is outside the ordering — it never enters best/worst.

**Update lifecycle.** The runner loads `performance.json` at startup
into memory, runs the suite, and writes the merged result at the end
of the run. Per case:

1. If the case was **executed** this run:
   - `latest` ← this run's verdict + details.
   - `best` ← more-favorable of (existing best, this latest); tie
     → keep existing.
   - `worst` ← less-favorable of (existing worst, this latest); tie
     → keep existing.
2. If the case was **skipped** this run:
   - `latest` ← `{verdict: "skipped", skipReason: <reason>}`.
   - `best` and `worst` ← **unchanged**.
3. If the case was **ignored with `drop: true`** this run:
   - The entry is **deleted** from `cases`.

**Row generation for the report.** The §5.1 row set is the keys of
`cases` after the run's merge. A case appearing in `cases` but no
longer in this run's expanded set (e.g. scenario deleted, or service
removed from sequence) still rows — its `latest` reflects whatever
was last recorded, often a pass from an older run. To purge such
orphans, use `drop: true` on the relevant `mishaps.ignore` entry,
or delete the entry from `cases` directly.

**Merge strategy for conflicts.** Two devs running the suite locally
on the same branch will produce conflicting writes to
`performance.json`. Resolution is fully deterministic and JSON-aware
— no `<<<<<<<` markers — via a `merge-performance` subcommand of
the integration suite itself (same binary, exposed as a Makefile
target for convenience as a git custom merge driver hook):

```
make -C tools/integration-suite merge-performance
```

This walks both sides and applies, per case ID:

- `latest`: later `ranAt` wins. If only one side has the case, use
  that side's `latest`.
- `best`: more-favorable verdict wins; tie → earlier `ranAt`.
- `worst`: less-favorable verdict wins; tie → later `ranAt`.
- If a case exists on only one side, take that side's entry whole.

The merge is associative and commutative — applying it in any order
across N branches gives the same result. Git can be configured to
use it as a custom merge driver via `.gitattributes`:

```
docs/integration-suite-v2/performance.json  merge=performance-json
```

with the driver defined in `.git/config` or `.gitconfig` per the
[git custom merge driver docs](https://git-scm.com/docs/gitattributes#_defining_a_custom_merge_driver).
Setup is documented in the suite's `tools/integration-suite/README.md`.
Without the custom driver, devs can still resolve conflicts
manually by running `make merge-performance` on both sides and
committing the result.

**Rendering is built into the run.** Every suite run ends by
writing `performance.json` and then rendering `last-run.md` from
it. There's no separate render step — the markdown is always in
sync with the JSON because both are produced by the same run. The
JSON is the substrate, the markdown is a view; both land at the
same time.

---

## 6. Blindspot interaction

V1's blindspot register (`docs/integration-suite-v1/blindspots.md`)
is gone in v2 — the parent design replaced it with "if the design is
silent, STOP." Mishaps inherit this stance:

- A scenario whose perturbation-applied case fails because the design
  doc is silent on what should happen under the perturbation is a
  **scenario authoring bug**, not a system defect. The author either
  adds the relevant kind to this scenario's `mishaps.ignore` list
  with a citation showing the design doesn't cover it, or finds the
  design statement that says what should happen.
- `mishaps.ignore` opts a scenario out of running specific mishap
  kinds (§4.3.3). It's history-aware: `mishaps.ignore: [crash]`
  prevents any new `x≠None` cases from running, but any pre-existing
  rows from prior runs stay in the report as `skipped (ignored:
  crash)` so historical best/worst is preserved. Use `drop: true` on
  an ignore entry to also purge prior history. Useful when a
  scenario tests a path that has no defined behavior under that
  perturbation.
- The reporter flags uncited perturbation failures as `fail (uncited
  expectation)` rather than plain `fail`, to disambiguate them from
  real defects.

This keeps the v2 "cited or absent" discipline intact under
perturbation: a case failing for a documented reason is a real
finding; a case failing because we don't actually know what should
happen is a coverage gap.

---

## 7. Open questions

1. **Crash variants beyond `docker restart`.** Part-2 ships only
   SIGTERM + cold start (`docker restart` with the platform's 10s
   grace). SIGKILL (no graceful drain) probes a different recovery
   class — half-acked JetStream messages, mid-write Mongo state with
   no shutdown hook. Adding a `crash-kill` sibling is a candidate
   for Part-2.5: same kind=per-pod loop axis, one extra value, no
   structural change to the runner.

2. **Pause duration sweep.** `mongo-pause-500ms` hardcodes 500ms.
   Longer pauses (5s, 30s) would exercise different timeouts — the
   chat services have a 10s default Cassandra timeout, for example.
   For Part-2 we ship one duration; a second duration becomes a
   sibling per-pod kind (`mongo-pause-5s`) with its own YAML +
   factory.

3. **Concurrent N>3.** Parent design says "3 fixed, no other
   levels." If a future scenario class is sensitive to higher
   concurrency (e.g. mass-message broadcast fan-out), we add a
   `concurrent-fan-out-10` as a separate sequence-level kind. The
   concurrent axis stops being binary; it becomes "off / 3 / 10."
   Reporter naming generalizes: `c=0|3|10`.

4. **Cross-site mishaps.** Per-pod kinds target the docker-local
   single-site stack. Multi-site kinds (`partition-sites`,
   `lag-outbox`) are Part-3 work; they'd add a new category
   ("per-site-pair") with its own predicate. The §2.1 split (per-pod
   / sequence-level) extends naturally — per-site-pair is just a
   third category.

---

## 8. Build order

When this spec is turned into a plan, the suggested implementation
order is below. **Each step is independently mergeable, and the
suite stays green at every step** — un-built mishap kinds are
simply not in the catalog, so the runner emits the happy case for
every scenario and that's all. Concrete cases activate as kinds
land.

1. **Executor interface + registry (§3.1, §3.3).** Replace the
   contents of `internal/mishap/types.go` with the Executor /
   Mishap / Factory shapes from §3.1; the Apply signature is
   `Apply(ctx, trigger <-chan struct{}) error` with `trigger == nil`
   for sequence-level kinds. Add `registry.go` keyed by kind name
   for both factories and pod predicates. Delete the legacy
   `ConcurrentExecutor` stub and `expander.go`'s power-set
   expander. **Red: no concrete factories registered yet; the
   registry has zero kinds.**

2. **DockerCLI (§3.3).** Implement the small interface
   (`Restart`, `Pause`, `Unpause`, `StartIfStopped`,
   `UnpauseIfPaused`) wrapping `exec.Command("docker", …)` in
   `internal/mishap/docker.go`. Add a preflight check to the
   suite's existing Makefile target that every container named in
   `catalogs/services/*.yaml`'s `container:` field is present in
   `docker ps`; fail-fast otherwise. Unit-test with a fake exec
   shim.

3. **Service ability profile additions (§4.2).** Add `container:`
   and `mishap_eligible:` fields to every
   `catalogs/services/*.yaml`. Derive `uses_mongo` from each
   service's existing `triggers[*].on_trigger[*].writes`/`reads`
   entries (no new field). Extend the validator CLI to enforce
   presence/well-formedness and to cross-check `factory:` and
   `eligible:` names in catalog mishap YAMLs against the registry.

4. **Observer trigger plumbing.** Add
   `OnFirstInScope(traceID, ownerPod string, ch chan<- struct{})`
   to the observer. **One-shot per `(traceID, ownerPod)` tuple per
   §2.2** — the runner makes one registration per trace under c=1
   (three registrations, three channels). Existing classification
   path stays unchanged. Unit-test the trigger via a fake event
   stream covering: matching event fires; subsequent matching
   events on the same trace do NOT re-fire; events on different
   traces don't fire this registration.

5. **`performance.json` substrate + reporter renderer (§5.1, §5.2,
   §5.4).** Land the data file as the source of truth before any
   case execution depends on it. Includes:
   - JSON schema, marshal/unmarshal types.
   - Load-at-start, write-at-end IO in the run lifecycle.
   - Merge subcommand on the suite binary (also exposed as
     `make merge-performance`) implementing the deterministic
     per-case merge from §5.4.
   - End-of-run renderer producing `last-run.md` as a view of
     `performance.json` — rows, latest/best/worst columns,
     granular skip reasons, per-attempt error blocks under failed
     rows, `(N↓)` relaxation annotation, perturbation-defects
     highlight section.
   - Optional git custom merge driver setup documented in
     `tools/integration-suite/README.md`.
   - First commit ships an empty `performance.json` (`{cases: {},
     schemaVersion: 1}`). Existing happy-only runs start populating
     it on the next run.

6. **Case expansion + naming (§4.3, §4.3.2, §4.4).** Implement
   `ExpandCases(scenario, services) []Case` with the None-first
   nested loop (`c → m → x`) and the deterministic case-ID format
   `<scenario.id>[c=<0|1> m=<pod|-> x=<pod|->]`. At this point the
   full case grid is generated per scenario, but only the happy
   case actually executes — every other case lacks an Executor and
   short-circuits via §4.3.3 ignore-style behavior (treated as if
   its kind were silently ignored, no skip row yet). The runner
   keeps writing happy-only rows into `performance.json`.

7. **Runner with skip rules + history-aware ignore (§4.3.1,
   §4.3.3).** Replace the existing per-scenario iteration with the
   phased runner: happy → single-axis c=0 → combined c=0 → all
   c=1. After each phase, evaluate skip rules H → X → M → P (first
   match wins) against verdicts seen so far. Before queuing any
   case for execution, consult `scenario.mishaps.ignore` against
   `performance.json` per §4.3.3 (ignored-with-history → skipped
   row preserving best/worst; ignored-without-history → omit;
   `drop: true` → delete entry). Skipped rows materialize with
   granular reasons. **Still green: kinds aren't built yet, so
   every non-happy case shows `skipped (ignored: <kind>)` (or
   simply doesn't row, if no history exists).**

8. **`runOneCase` with no-fail-fast + retry-on-fail (§4.6 +
   §4.6.1).** Implement the per-case attempt loop: build Executors
   for non-trivial axes, allocate one trigger channel per
   `(traceID, target pod)` tuple, spawn one Apply goroutine per
   channel, defer Cleanup; collect failures in occurrence order
   within an attempt without fail-fasting. On fail, retry up to
   3 total attempts (1 initial + 2 retries) with fresh traceIDs
   and channels; on all-fail, write all 3 error blocks verbatim
   into the case's `latest.errorBlocks`. On any attempt's pass,
   record pass and return immediately. Wire `verdict.go` to apply
   §4.6 per-axis rules (timeframe discard under any non-trivial
   axis, quietGrace = 1s, anomaly relaxation under x≠None,
   per-actor all-pass under c=1).

9. **`crash` kind (§3.2.1).** Land `internal/mishap/crash.go`.
   Register the factory + the `all-pods` predicate. With this and
   the prior steps, the runner can now execute every
   `x≠None`-touching case. End-to-end test against docker-local on
   `room-create-001`. **Green for `[c=0 m=- x=room-service]` and
   `[c=0 m=- x=room-worker]`** (and the combined `[c=0 m=POD
   x=POD]` cases if they don't blow up — those become real
   passing/failing rows, not skipped placeholders). Rule X
   propagation starts firing for real.

10. **`mongo-pause-500ms` kind (§3.2.2).** Land
    `internal/mishap/mongo_pause.go` plus the `mongo-using-pods`
    predicate. All `m≠None` cases activate; same-pod combined `m=X
    x=X` cases now execute end-to-end. Rule M propagation starts
    firing. No runner changes needed — composes the existing
    toolkit (DockerCLI Pause + time.Sleep + Unpause).

11. **`concurrent-fan-out-3` kind (§3.2.3).** Land
    `internal/mishap/concurrent.go`. Touches:
    - `dispatcher.go` — new `FireWithActor(ctx, scenario, actor)`
      so the executor can drive per-actor fires. Each call
      produces a fresh traceID via the existing dispatcher
      mechanism (per-attempt isolation from §4.6.1).
    - Three separate `OnFirstInScope` registrations (one per
      actor's traceID, all targeting the same pod when composed
      with a per-pod mishap) — each fires its own perturbation
      once, per §2.2.
    - `verdict.go` — per-actor all-pass rule from §4.6 (case
      passes iff every actor's reads close cleanly).
    - Reporter — `2/2 ×3` notation for per-actor read tallies in
      the `latest.readsMatched` field.
    - Pairwise c=1 skip rule (Rule P) starts pruning the c=1
      column.
    This is the hardest step because of multi-axis composition;
    lands last.

After step 11, the full §4.3 case grid is live for every scenario.
Scenario authors don't have to do anything to opt in — every
scenario gains its case grid automatically. The lever to bound
case count remains `scenario.mishaps.ignore` per §4.3.3.
