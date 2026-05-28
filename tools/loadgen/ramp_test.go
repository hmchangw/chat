package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRPSSteps(t *testing.T) {
	tests := []struct {
		in      string
		want    []int
		wantErr bool
	}{
		{in: "500,1000,2000", want: []int{500, 1000, 2000}},
		{in: "1k,2k,5k", want: []int{1000, 2000, 5000}},
		{in: " 500 , 1k ", want: []int{500, 1000}},
		{in: "1000", want: []int{1000}},
		{in: "", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "1000,500", wantErr: true},  // not ascending
		{in: "0,1000", wantErr: true},    // not positive
		{in: "1000,1000", wantErr: true}, // not strictly ascending
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseRPSSteps(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// fakeWorkload returns canned inputs, one per step, in order.
type fakeWorkload struct {
	inputs []rpsStepInputs
	calls  int
}

func (f *fakeWorkload) Label() string { return "fake" }
func (f *fakeWorkload) RunStep(_ context.Context, _ int, _, _ time.Duration) (rpsStepInputs, error) {
	in := f.inputs[f.calls]
	f.calls++
	return in, nil
}

func passInputs(target int) rpsStepInputs {
	return rpsStepInputs{TargetRPS: target, Hold: time.Second, AttemptedOps: target,
		Latencies: []seriesSamples{{Name: "E1", Samples: nLatencies(10, ms(20))}}}
}

func tripInputs(target int) rpsStepInputs {
	return rpsStepInputs{TargetRPS: target, Hold: time.Second, AttemptedOps: target,
		Latencies: []seriesSamples{{Name: "E1", Samples: nLatencies(10, ms(400))}}}
}

func inconclusiveInputs(target int) rpsStepInputs {
	return rpsStepInputs{TargetRPS: target, Hold: time.Second, AttemptedOps: target / 2,
		Latencies: []seriesSamples{{Name: "E1", Samples: nLatencies(10, ms(20))}}}
}

func TestRunRamp_StopsOnTrip(t *testing.T) {
	w := &fakeWorkload{inputs: []rpsStepInputs{passInputs(500), tripInputs(1000), passInputs(2000)}}
	cfg := rampConfig{Steps: []int{500, 1000, 2000}, Hold: time.Second,
		Thresholds: defaultRPSThresholds(), StopOnTrip: true}
	results := runRamp(context.Background(), w, &cfg)
	require.Len(t, results, 2) // stopped after the trip at 1000
	assert.Equal(t, verdictPass, results[0].Kind)
	assert.Equal(t, verdictTrip, results[1].Kind)
	assert.Equal(t, 2, w.calls)
}

func TestRunRamp_DoesNotStopOnInconclusive(t *testing.T) {
	w := &fakeWorkload{inputs: []rpsStepInputs{passInputs(500), inconclusiveInputs(1000), passInputs(2000)}}
	cfg := rampConfig{Steps: []int{500, 1000, 2000}, Hold: time.Second,
		Thresholds: defaultRPSThresholds(), StopOnTrip: true}
	results := runRamp(context.Background(), w, &cfg)
	require.Len(t, results, 3)
	assert.Equal(t, verdictInconclusive, results[1].Kind)
	assert.Equal(t, verdictPass, results[2].Kind)
}

func TestRunRamp_NoStopOnTripRunsAll(t *testing.T) {
	w := &fakeWorkload{inputs: []rpsStepInputs{passInputs(500), tripInputs(1000), tripInputs(2000)}}
	cfg := rampConfig{Steps: []int{500, 1000, 2000}, Hold: time.Second,
		Thresholds: defaultRPSThresholds(), StopOnTrip: false}
	results := runRamp(context.Background(), w, &cfg)
	require.Len(t, results, 3)
}

func TestMaxRPSExitCode(t *testing.T) {
	pass := []rpsStepResult{{Kind: verdictPass}, {Kind: verdictTrip}}
	none := []rpsStepResult{{Kind: verdictInconclusive}, {Kind: verdictTrip}}
	assert.Equal(t, 0, maxRPSExitCode(pass))
	assert.Equal(t, 1, maxRPSExitCode(none))
	assert.Equal(t, 1, maxRPSExitCode(nil))
}

func TestWaitOrCancel(t *testing.T) {
	require.NoError(t, waitOrCancel(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Error(t, waitOrCancel(ctx, time.Hour))
}
