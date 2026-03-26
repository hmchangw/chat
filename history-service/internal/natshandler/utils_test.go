package natshandler

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testReq struct {
	Name string `json:"name"`
}

type testResp struct {
	Greeting string `json:"greeting"`
}

func TestHandleRequest_UnmarshalSuccess(t *testing.T) {
	req := testReq{Name: "world"}
	data, _ := json.Marshal(req)

	var captured testReq
	handler := func(ctx context.Context, r testReq) (*testResp, error) {
		captured = r
		return &testResp{Greeting: "hello " + r.Name}, nil
	}

	var parsed testReq
	err := json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	resp, err := handler(context.Background(), parsed)
	require.NoError(t, err)
	assert.Equal(t, "world", captured.Name)
	assert.Equal(t, "hello world", resp.Greeting)
}

func TestHandleRequest_HandlerError(t *testing.T) {
	handler := func(ctx context.Context, r testReq) (*testResp, error) {
		return nil, fmt.Errorf("something broke")
	}

	_, err := handler(context.Background(), testReq{Name: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "something broke")
}

func TestHandleRequest_InvalidJSON(t *testing.T) {
	var parsed testReq
	err := json.Unmarshal([]byte("not json"), &parsed)
	require.Error(t, err)
}
