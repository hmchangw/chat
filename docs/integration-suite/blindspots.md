# Blindspots Register

Every `@blindspot:<slug>` used in any feature file under
`tools/integration-suite/features/` must have a matching level-2
heading (`## <slug>`) in this file.

Run `make integration-suite-lint` to verify consistency.

<!-- Entries follow this template:

## <slug>

**Found in:** features/<scope>/<feature>.feature
**Question:** What is the documented expected behavior for ...?
**Candidates:**
  - …
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future>.md
**Status:** open
**Added:** YYYY-MM-DD

-->
