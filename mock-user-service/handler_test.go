package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/natsrouter"
)

func newCtx(params map[string]string) *natsrouter.Context {
	return natsrouter.NewContext(params)
}

func TestHandler_CheckSite(t *testing.T) {
	h := NewHandler("site-local")

	t.Run("match", func(t *testing.T) {
		err := h.checkSite(newCtx(map[string]string{"siteID": "site-local"}))
		assert.NoError(t, err)
	})

	t.Run("mismatch returns ErrNotFound", func(t *testing.T) {
		err := h.checkSite(newCtx(map[string]string{"siteID": "site-other"}))
		require.Error(t, err)
		var routeErr *natsrouter.RouteError
		require.True(t, errors.As(err, &routeErr), "want *natsrouter.RouteError, got %T", err)
		assert.Equal(t, natsrouter.CodeNotFound, routeErr.Code)
	})
}

func TestBuildMockSub(t *testing.T) {
	sub := buildMockSub("alice", "site-local")
	assert.Equal(t, "alice", sub.User.Account)
	assert.Equal(t, "site-local", sub.SiteID)
	assert.NotEmpty(t, sub.ID)
	assert.NotEmpty(t, sub.RoomID)
}

func TestBuildMockApp(t *testing.T) {
	app := buildMockApp("app-1", "Mock One")
	assert.Equal(t, "app-1", app.ID)
	assert.Equal(t, "Mock One", app.Name)
}
