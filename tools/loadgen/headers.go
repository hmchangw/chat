package main

// HeaderRunID is set on every loadgen-published NATS message so concurrent
// runs against the same SUT can filter incoming canonical/broadcast traffic
// to their own run (see Task 1b.2 run isolation).
const HeaderRunID = "X-Loadgen-Run-ID"
