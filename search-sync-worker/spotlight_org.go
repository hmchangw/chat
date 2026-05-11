package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// spotlightOrgCollection maintains the spotlight-org ES index, one
// document per sectId, sourced from hr-syncer's batched HR account
// publishes. The doc ID is the sectId; many employees collapse to one
// document via dedup in BuildAction. The HR_SYNC stream is owned by
// hr-syncer — this collection is a pure consumer.
type spotlightOrgCollection struct {
	indexName string
	devMode   bool
}

func newSpotlightOrgCollection(indexName string, devMode bool) *spotlightOrgCollection {
	return &spotlightOrgCollection{indexName: indexName, devMode: devMode}
}

func (c *spotlightOrgCollection) StreamConfig(siteID string) jetstream.StreamConfig {
	cfg := stream.HRSync(siteID)
	return jetstream.StreamConfig{Name: cfg.Name, Subjects: cfg.Subjects}
}

func (c *spotlightOrgCollection) ConsumerName() string {
	return "spotlight-org-sync"
}

func (c *spotlightOrgCollection) FilterSubjects(siteID string) []string {
	return []string{subject.HRSyncEmployeesUpsert(siteID)}
}

func (c *spotlightOrgCollection) TemplateName() string {
	return "spotlight_org_template"
}

func (c *spotlightOrgCollection) TemplateBody() json.RawMessage {
	return spotlightOrgTemplateBody(c.indexName, c.devMode)
}

// BuildAction parses an HR sync envelope, dedupes by SectID (last-wins),
// and emits one ES _update per unique sectId with doc_as_upsert:true.
// Doc-merge plus omitempty means partial-field publishes preserve the
// stored values for unset fields. Empty SectID rows and empty batches
// return (nil, nil) so the handler acks with no ES write.
func (c *spotlightOrgCollection) BuildAction(data []byte) ([]searchengine.BulkAction, error) {
	var envelope model.HRSyncEvent
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal hr-sync envelope: %w", err)
	}
	if envelope.Timestamp <= 0 {
		return nil, fmt.Errorf("build spotlight-org action: missing timestamp")
	}

	var employees []SpotlightOrgIndex
	if envelope.Gzip {
		var compressed []byte
		if err := json.Unmarshal(envelope.Payload, &compressed); err != nil {
			return nil, fmt.Errorf("decode gzip payload base64: %w", err)
		}
		decompressed, err := gunzipBytes(compressed)
		if err != nil {
			return nil, fmt.Errorf("decompress hr-sync payload: %w", err)
		}
		if err := json.Unmarshal(decompressed, &employees); err != nil {
			return nil, fmt.Errorf("unmarshal hr-sync employees: %w", err)
		}
	} else if err := json.Unmarshal(envelope.Payload, &employees); err != nil {
		return nil, fmt.Errorf("unmarshal hr-sync employees: %w", err)
	}
	if len(employees) == 0 {
		return nil, nil
	}

	// Empty SectID rows are skipped silently (employees not yet
	// assigned to a section). Last write per sectId wins within a batch.
	deduped := make(map[string]*SpotlightOrgIndex, len(employees))
	order := make([]string, 0, len(employees))
	for i := range employees {
		emp := &employees[i]
		if emp.SectID == "" {
			continue
		}
		if _, seen := deduped[emp.SectID]; !seen {
			order = append(order, emp.SectID)
		}
		deduped[emp.SectID] = emp
	}
	if len(deduped) == 0 {
		return nil, nil
	}

	actions := make([]searchengine.BulkAction, 0, len(deduped))
	for _, sectID := range order {
		body, err := buildSpotlightOrgUpdateBody(deduped[sectID])
		if err != nil {
			return nil, err
		}
		actions = append(actions, searchengine.BulkAction{
			Action: searchengine.ActionUpdate,
			Index:  c.indexName,
			DocID:  sectID,
			Doc:    body,
		})
	}
	return actions, nil
}

// buildSpotlightOrgUpdateBody wraps row in {doc, doc_as_upsert:true}
// for ES.
func buildSpotlightOrgUpdateBody(row *SpotlightOrgIndex) (json.RawMessage, error) {
	body := map[string]any{
		"doc":           row,
		"doc_as_upsert": true,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal spotlight-org update body: %w", err)
	}
	return data, nil
}

func gunzipBytes(b []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	return io.ReadAll(gr)
}

// SpotlightOrgIndex is the wire row, ES document body, and ES mapping
// source for the spotlight-org index. omitempty drives the partial-update
// contract: absent fields don't appear in the ES doc and doc-merge
// preserves the stored value. Fields not declared here are ignored when
// unmarshaling, so hr-syncer may publish additional fields.
type SpotlightOrgIndex struct {
	SectID          string `json:"sectId,omitempty"          es:"search_as_you_type,custom_analyzer"`
	SectTCName      string `json:"sectTCName,omitempty"      es:"search_as_you_type,custom_analyzer"`
	SectName        string `json:"sectName,omitempty"        es:"search_as_you_type,custom_analyzer"`
	SectDescription string `json:"sectDescription,omitempty" es:"search_as_you_type,custom_analyzer"`
	DeptID          string `json:"deptId,omitempty"          es:"search_as_you_type,custom_analyzer"`
	DeptTCName      string `json:"deptTCName,omitempty"      es:"search_as_you_type,custom_analyzer"`
	DeptName        string `json:"deptName,omitempty"        es:"search_as_you_type,custom_analyzer"`
	DeptDescription string `json:"deptDescription,omitempty" es:"search_as_you_type,custom_analyzer"`
	DivisionID      string `json:"divisionId,omitempty"      es:"search_as_you_type,custom_analyzer"`
}

func spotlightOrgTemplateBody(indexName string, devMode bool) json.RawMessage {
	shards, replicas := indexTopology(3, 1, devMode)
	tmpl := map[string]any{
		"index_patterns": []string{indexName},
		"template": map[string]any{
			"settings": map[string]any{
				"index": map[string]any{
					"number_of_shards":   shards,
					"number_of_replicas": replicas,
				},
				"analysis": customAnalyzerSettings(),
			},
			"mappings": map[string]any{
				"dynamic":    false,
				"properties": esPropertiesFromStruct[SpotlightOrgIndex](),
			},
		},
	}
	data, _ := json.Marshal(tmpl)
	return data
}
