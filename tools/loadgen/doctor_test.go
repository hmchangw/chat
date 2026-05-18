package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunDoctor_RunsAllChecks(t *testing.T) {
	var buf bytes.Buffer
	// Doctor returns 0 if all checks pass or 1 if any fail.
	// In test env, some checks (docker, free mem) may fail; we just verify
	// the function runs and produces output.
	_ = runDoctorTo(&buf, nil)
	out := buf.String()
	assert.Contains(t, out, "go binary")
	assert.Contains(t, out, "free memory")
}

func TestRunDoctor_ProducesHeader(t *testing.T) {
	var buf bytes.Buffer
	_ = runDoctorTo(&buf, nil)
	out := buf.String()
	assert.Contains(t, out, "loadgen doctor")
}

func TestRunDoctor_ShowsDiskCheck(t *testing.T) {
	var buf bytes.Buffer
	_ = runDoctorTo(&buf, nil)
	out := buf.String()
	assert.Contains(t, out, "disk space")
}

func TestRunDoctor_ShowsDockerCheck(t *testing.T) {
	var buf bytes.Buffer
	_ = runDoctorTo(&buf, nil)
	out := buf.String()
	assert.Contains(t, out, "docker")
}
