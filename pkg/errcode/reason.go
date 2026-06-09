package errcode

// Reason is the wire `reason` field: an open set of domain-specific machine
// codes the frontend switches on. Concrete reasons live in codes_<service>.go.
type Reason string
