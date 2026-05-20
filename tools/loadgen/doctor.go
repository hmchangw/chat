// tools/loadgen/doctor.go
//
// `loadgen doctor` subcommand — preflight host-readiness checks
// (cross-phase Task X.1).
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
)

func runDoctor(_ *config, args []string) int {
	return runDoctorTo(os.Stdout, args)
}

type doctorCheck struct {
	Name   string
	Status string // "ok", "warn", "fail"
	Detail string
}

func runDoctorTo(w io.Writer, _ []string) int {
	checks := []doctorCheck{
		checkGoBinary(),
		checkFreeMemory(),
		checkDiskSpace(),
		checkPortAvailable("metrics", ":9099"),
		checkDockerAvailable(),
		checkDockerCompose(),
	}

	fmt.Fprintln(w, "loadgen doctor — host readiness checks")
	fmt.Fprintln(w, "")

	failures := 0
	for _, c := range checks {
		var symbol string
		switch c.Status {
		case "warn":
			symbol = "!"
		case "fail":
			symbol = "x"
			failures++
		default:
			symbol = "ok"
		}
		fmt.Fprintf(w, "  [%s]  %-20s  %s\n", symbol, c.Name, c.Detail)
	}
	fmt.Fprintln(w, "")
	if failures == 0 {
		fmt.Fprintln(w, "All checks passed.")
		return 0
	}
	fmt.Fprintf(w, "%d checks failed — see remediation hints in USAGE.md.\n", failures)
	return 1
}

func checkGoBinary() doctorCheck {
	if path, err := exec.LookPath("go"); err == nil {
		return doctorCheck{Name: "go binary", Status: "ok", Detail: path}
	}
	return doctorCheck{Name: "go binary", Status: "warn", Detail: "not on PATH (only needed for building from source)"}
}

func checkFreeMemory() doctorCheck {
	if mb, ok := readMemAvailableMB(); ok {
		if mb < 500 {
			return doctorCheck{Name: "free memory", Status: "warn", Detail: fmt.Sprintf("%d MB (low — may not fit realistic preset)", mb)}
		}
		return doctorCheck{Name: "free memory", Status: "ok", Detail: fmt.Sprintf("%d MB", mb)}
	}
	return doctorCheck{Name: "free memory", Status: "warn", Detail: "unable to determine (non-Linux host)"}
}

func checkDiskSpace() doctorCheck {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(".", &stat); err != nil {
		return doctorCheck{Name: "disk space", Status: "warn", Detail: "unable to determine"}
	}
	// #nosec G115 -- Bavail/Bsize are filesystem free-block counts from statfs; product in MB comfortably fits int on any host loadgen runs on
	freeMB := int(stat.Bavail) * int(stat.Bsize) / (1024 * 1024)
	if freeMB < 1024 {
		return doctorCheck{Name: "disk space", Status: "warn", Detail: fmt.Sprintf("%d MB free in cwd (low)", freeMB)}
	}
	return doctorCheck{Name: "disk space", Status: "ok", Detail: fmt.Sprintf("%d MB free in cwd", freeMB)}
}

func checkPortAvailable(name, addr string) doctorCheck {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return doctorCheck{Name: "metrics port", Status: "warn",
			Detail: fmt.Sprintf("%s in use: %v (loadgen may fail to start)", addr, err)}
	}
	_ = ln.Close()
	return doctorCheck{Name: "metrics port", Status: "ok", Detail: addr + " available"}
}

func checkDockerAvailable() doctorCheck {
	cmd := exec.Command("docker", "version", "--format", "{{.Client.Version}}")
	out, err := cmd.Output()
	if err != nil {
		return doctorCheck{Name: "docker client", Status: "warn", Detail: "not on PATH (only needed for compose-driven test runs)"}
	}
	return doctorCheck{Name: "docker client", Status: "ok", Detail: "v" + trimNewlines(string(out))}
}

func checkDockerCompose() doctorCheck {
	cmd := exec.Command("docker", "compose", "version", "--short")
	out, err := cmd.Output()
	if err != nil {
		return doctorCheck{Name: "docker compose", Status: "warn", Detail: "not available"}
	}
	return doctorCheck{Name: "docker compose", Status: "ok", Detail: "v" + trimNewlines(string(out))}
}

func trimNewlines(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
