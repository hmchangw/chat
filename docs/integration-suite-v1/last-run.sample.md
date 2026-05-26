Run:        2026-05-19T14:22:11Z   (runID 7a2c)
Total:      58 scenarios
Duration:   3m47s

APPROVED   42 scenarios       ← authoritative; gates CI
  Passed:        39
  Failed:         2
    HandlerError:  1
    Persistence:   1
  Blindspot:      1
  Score:        92.9%   (39 / 42)

DRAFT       16 scenarios       ← informational; never gates
  Passed:        11
  Failed:         3
    Validation:    1
    Timeout:       2
  Blindspot:      2
  Score:        68.8%   (11 / 16)

Last audit: 2026-04-29 (n=30) — accuracy 93.3%, FP 6.7%, FN 3.3%

Failures (behavior diverged from design)
  [APPROVED] features/service/room-role-update.feature:42 "Promoting an existing owner is rejected"
    class: HandlerError    code: ALREADY_OWNER
    trace: 4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7
  [APPROVED] features/service/room.feature:18 "DM room ID is deterministic across reorder"
    class: Persistence
    trace: 9f2b5c91a7e4d8f6b3a2c1e9d7b8a4f3
  [DRAFT] features/service/history-thread.feature:34 "Empty threadMessageId is rejected"
    class: Validation
    trace: 1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d
  [DRAFT] features/pipeline/message-submission.feature:22 "Submitted message persists to history"
    class: Timeout
    trace: 7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b
  [DRAFT] features/pipeline/message-submission.feature:31 "Submitted message broadcasts to room members"
    class: Timeout
    trace: 3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f

Blindspots (undocumented behavior — design owes an answer)
  [APPROVED] features/service/history-thread.feature: history-forbidden-class-unverifiable
  [DRAFT]    features/pipeline/message-submission.feature: message-submit-needs-jetstream-primitive
  [DRAFT]    features/regional-resilience/partial-nats-tw.feature: partial-history-contract

COVERAGE (documented behavior cases — report-only)
  history      covered 4/7   known-gap 1   uncovered 2    57.1%
  messaging    covered 1/3   known-gap 2   uncovered 0    33.3%
  room         covered 7/9   known-gap 0   uncovered 2    77.8%
  TOTAL        covered 12/19  known-gap 3   uncovered 4    63.2%

  Uncovered (documented, no passing @covers scenario, not a known gap):
    [history] history-thread-invalid-cursor — An invalid pagination cursor is rejected as bad_request.
    [history] history-threadparent-invalid-filter — An invalid thread filter is rejected as bad_request.
    [room]    room-roleupdate-not-channel — Role update on a non-channel room is rejected.
    [room]    room-roleupdate-target-not-member — Updating the role of a non-member is rejected.
