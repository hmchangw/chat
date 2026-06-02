package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromClient_RangeQuery_ParsesMatrix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/query_range", r.URL.Path)
		assert.NotEmpty(t, r.URL.Query().Get("query"))
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{"resultType":"matrix","result":[
				{"metric":{"container_label_com_docker_compose_service":"cassandra"},
				 "values":[[100,"10.5"],[105,"11.0"]]}
			]}}`))
	}))
	defer srv.Close()

	c := newPromClient(srv.URL)
	start := time.Unix(100, 0)
	series, err := c.RangeQuery(context.Background(), `up`, start, start.Add(5*time.Second), 5*time.Second)
	require.NoError(t, err)
	require.Len(t, series, 1)
	assert.Equal(t, "cassandra", series[0].Labels["container_label_com_docker_compose_service"])
	require.Len(t, series[0].Samples, 2)
	assert.Equal(t, 10.5, series[0].Samples[0].V)
	assert.Equal(t, 11.0, series[0].Samples[1].V)
	assert.Equal(t, time.Unix(105, 0).UTC(), series[0].Samples[1].T.UTC())
}

func TestPromClient_RangeQuery_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"boom"}`))
	}))
	defer srv.Close()

	_, err := newPromClient(srv.URL).RangeQuery(context.Background(), `up`, time.Unix(0, 0), time.Unix(5, 0), time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}
