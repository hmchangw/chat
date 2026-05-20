package main

import (
	randv2 "math/rand/v2"
	"sync"
	"time"
)

// senderPicker returns an index into the per-Generator subscriptions
// slice each time pick() is called. Uniform draws use the lock-free
// math/rand/v2 globals (S4); Zipf draws need a seeded *randv2.Rand
// because randv2.Zipf is bound to a specific source. Bug 4: pre-fix
// every preset was implicitly uniform regardless of SenderDist.
type senderPicker struct {
	n    int
	dist Distribution

	// zipf state. nil for non-Zipf distributions. Guarded by mu because
	// the open-loop generator dispatches publishOne onto a goroutine
	// pool when MaxInFlight>0, so multiple publishOne calls may race
	// for pick(); randv2.Zipf is documented as not safe for concurrent
	// use.
	mu   sync.Mutex
	zipf *randv2.Zipf
}

// newSenderPicker builds a picker for n subscriptions under the given
// distribution. The seed is honored only for the Zipf branch; uniform
// uses the global randv2 source (matches S4's deliberate non-determinism
// trade-off for per-tick picks).
//
// Zipf parameters: s=1.07 (just above the s>1 minimum), v=1.0 produces
// the canonical "head dominates" shape with a long tail across n
// senders. imax=n-1 caps the draw inside the slice bounds.
func newSenderPicker(n int, dist Distribution, seed int64) *senderPicker {
	sp := &senderPicker{n: n, dist: dist}
	if dist == DistZipf && n > 1 {
		// PCG seeded from the user's --seed; using two derived halves
		// keeps repro-ability while not duplicating fixture seeding.
		// #nosec G115 -- seed is operator-supplied int64; reinterpreting bits as uint64 PCG seed is intentional, value range is irrelevant for a PRNG seed
		src := randv2.NewPCG(uint64(seed), uint64(seed)^0xDEADBEEF)
		// #nosec G404 -- load-test Zipf sender distribution; reproducibility via --seed requires deterministic math/rand, no security context
		r := randv2.New(src)
		sp.zipf = randv2.NewZipf(r, 1.07, 1.0, uint64(n-1))
	}
	return sp
}

// pick returns an index in [0, n). Empty pickers return 0; callers must
// guard with len(subs)==0 before calling.
func (sp *senderPicker) pick() int {
	if sp.n <= 0 {
		return 0
	}
	if sp.zipf != nil {
		sp.mu.Lock()
		v := sp.zipf.Uint64()
		sp.mu.Unlock()
		// #nosec G115 -- Zipf is constructed with imax=n-1 (an int>0), so v fits in int; defensive cap below protects against any boundary surprise
		idx := int(v)
		if idx >= sp.n {
			// Defensive — Zipf is bounded by imax=n-1 but be paranoid.
			idx = sp.n - 1
		}
		return idx
	}
	// #nosec G404 -- load-test uniform sender picker; uniform draw over fixture subscription pool, no security context
	return randv2.IntN(sp.n)
}

// threadPool is a fixed-capacity ring buffer of recently published
// message IDs. Used by Bug 4 part B to back ThreadRate: with probability
// ThreadRate, publishOne picks a parent ID from the pool to reply to.
//
// The buffer is capped so memory stays bounded under long runs at high
// rates; the most recent ~capacity messages are eligible parents, which
// keeps the simulated thread depth realistic (real users don't reply
// to ancient messages).
type threadPool struct {
	mu       sync.Mutex
	capacity int
	ids      []string
	created  []time.Time
	pos      int
	full     bool
}

func newThreadPool(capacity int) *threadPool {
	if capacity <= 0 {
		capacity = 1
	}
	return &threadPool{
		capacity: capacity,
		ids:      make([]string, capacity),
		created:  make([]time.Time, capacity),
	}
}

func (tp *threadPool) add(id string) {
	if id == "" {
		return
	}
	tp.mu.Lock()
	tp.ids[tp.pos] = id
	tp.created[tp.pos] = time.Now().UTC()
	tp.pos = (tp.pos + 1) % tp.capacity
	if tp.pos == 0 {
		tp.full = true
	}
	tp.mu.Unlock()
}

// size reports the count of stored entries (0..capacity). Mostly for
// tests to assert the ring-buffer cap is respected.
func (tp *threadPool) size() int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if tp.full {
		return tp.capacity
	}
	return tp.pos
}

// maybeParent returns a randomly selected (id, created) pair from the
// pool with probability `rate`. Returns ("", zero-time) when:
//   - rate <= 0
//   - the pool is empty
//   - the random draw rejects this call
//
// The returned createdAt is the time the parent message was added to
// the pool — close enough to the SUT's stored CreatedAt for the
// downstream thread-fan-out path to recognize the reference.
func (tp *threadPool) maybeParent(rate float64) (string, time.Time) {
	if rate <= 0 {
		return "", time.Time{}
	}
	// #nosec G404 -- load-test thread-rate weighted coin flip; samples whether to thread-reply, no security context
	if randv2.Float64() >= rate {
		return "", time.Time{}
	}
	tp.mu.Lock()
	defer tp.mu.Unlock()
	n := tp.pos
	if tp.full {
		n = tp.capacity
	}
	if n == 0 {
		return "", time.Time{}
	}
	// #nosec G404 -- load-test thread-parent picker; uniform draw over recent-publish ring buffer, no security context
	idx := randv2.IntN(n)
	return tp.ids[idx], tp.created[idx]
}
