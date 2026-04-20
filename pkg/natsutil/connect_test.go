package natsutil_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestConnect_MissingCredsFileFailsFast(t *testing.T) {
	_, err := natsutil.Connect("nats://127.0.0.1:1", "/definitely/does/not/exist.creds")
	if err == nil {
		t.Fatal("expected error for missing creds file")
	}
	if !strings.Contains(err.Error(), "nats creds file") {
		t.Fatalf("expected creds-file error, got: %v", err)
	}
}

func TestConnect_PresentCredsFilePassesPrecheck(t *testing.T) {
	// A real connect would still fail (invalid creds content, bogus URL), but
	// the pre-check must succeed when the file exists. We assert by checking
	// the error does NOT come from the missing-file precondition.
	dir := t.TempDir()
	path := filepath.Join(dir, "fake.creds")
	if err := os.WriteFile(path, []byte("not-a-real-creds-file"), 0o600); err != nil {
		t.Fatalf("write temp creds: %v", err)
	}

	_, err := natsutil.Connect("nats://127.0.0.1:1", path)
	if err != nil && strings.Contains(err.Error(), "nats creds file") {
		t.Fatalf("precheck should pass when file exists, got: %v", err)
	}
}
