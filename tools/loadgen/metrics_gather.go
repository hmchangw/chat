package main

import (
	dto "github.com/prometheus/client_model/go"
)

// gatheredCounterValue sums counter values across all metrics in mfs that
// match the given name and (optionally) a single label filter. When
// labelName is empty, all time-series for that metric are summed.
//
// Used by runProgress (progress.go), executeRun (run.go), and several test
// helpers — centralised here so metric gathering has a single home.
func gatheredCounterValue(mfs []*dto.MetricFamily, name string, labelName, labelValue string) float64 {
	var total float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			if labelName == "" {
				total += metric.GetCounter().GetValue()
				continue
			}
			for _, l := range metric.GetLabel() {
				if l.GetName() == labelName && l.GetValue() == labelValue {
					total += metric.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}

// gatheredCounterLabelPair sums counter values where BOTH label pairs match.
// Used when a counter has multiple labels and the headline summary needs to
// filter on two dimensions simultaneously (e.g. phase="measured" AND reason="gatekeeper").
func gatheredCounterLabelPair(mfs []*dto.MetricFamily, name, label1Name, label1Value, label2Name, label2Value string) float64 {
	var total float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			match1, match2 := false, false
			for _, l := range metric.GetLabel() {
				if l.GetName() == label1Name && l.GetValue() == label1Value {
					match1 = true
				}
				if l.GetName() == label2Name && l.GetValue() == label2Value {
					match2 = true
				}
			}
			if match1 && match2 {
				total += metric.GetCounter().GetValue()
			}
		}
	}
	return total
}
