//go:build integration

package userstore

import (
	"os"
	"testing"

	"github.com/hmchangw/chat/pkg/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.TerminateAll()
	os.Exit(code)
}
