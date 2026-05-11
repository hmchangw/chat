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

// spotlightOrgCollection implements Collection for the spotlight-org
// search index. One document per `sectId` carries the nine org fields
// projected from each HR account row in the batched payload. The doc
// ID is the sectId itself — many employees collapse to one document
// via dedup in BuildAction.
//
// The wire-side row type and the ES doc projection are the same struct
// (SpotlightOrgIndex below), keeping the consumer loosely coupled to
// hr-syncer's own internal Employee/Org types without taking a public
// dependency on a pkg/model.Employee that would conflict on merge with
// the internal repo's existing one.
//
// The HR_SYNC_{siteID} stream is owned by `hr-syncer`; this collection
// is a pure consumer. main.go skips HR_SYNC in its bootstrap loop
// (added when the collection is wired in Task 14) the same way it
// skips INBOX.
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

// BuildAction parses an HR sync envelope, dedupes employees by SectID
// (keeping the last occurrence per sectId), and emits one ES `_update`
// per unique sectId with `doc_as_upsert:true`. Doc-merge means absent
// fields on the projection preserve the stored value rather than
// overwriting with empty strings — partial-field events from
// `hr-syncer` work without a painless script.
//
// Returns (nil, nil) for empty employee arrays or batches where every
// row has an empty SectID; the handler then acks the JS message with
// no ES write.
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
		// Gzip payloads are base64-encoded binary in the JSON envelope.
		// json.Unmarshal decodes the JSON string → raw bytes, then we
		// decompress to get the JSON array.
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
	} else {
		if err := json.Unmarshal(envelope.Payload, &employees); err != nil {
			return nil, fmt.Errorf("unmarshal hr-sync employees: %w", err)
		}
	}
	if len(employees) == 0 {
		return nil, nil
	}

	// Dedup by SectID keeping the LAST occurrence — within one batch
	// the most recent update wins. Empty SectID rows are skipped
	// silently (employees not yet assigned to a section).
	deduped := make(map[string]SpotlightOrgIndex, len(employees))
	order := make([]string, 0, len(employees))
	for i := range employees {
		emp := &employees[i]
		if emp.SectID == "" {
			continue
		}
		if _, seen := deduped[emp.SectID]; !seen {
			order = append(order, emp.SectID)
		}
		deduped[emp.SectID] = *emp
	}
	if len(deduped) == 0 {
		return nil, nil
	}

	actions := make([]searchengine.BulkAction, 0, len(deduped))
	for _, sectID := range order {
		row := deduped[sectID]
		body, err := buildSpotlightOrgUpdateBody(&row)
		if err != nil {
			return nil, err
		}
		actions = append(actions, searchengine.BulkAction{
			Action: searchengine.ActionUpdate,
			Index:  c.indexName,
			DocID:  sectID,
			Doc:    body,
			// No Version: ActionUpdate must not use external versioning.
			// handler.go::isBulkItemSuccess depends on this.
		})
	}
	return actions, nil
}

// buildSpotlightOrgUpdateBody wraps the row in an ES `_update` body
// with `doc_as_upsert:true`. The row is already the projection — the
// json `omitempty` discipline guarantees absent fields don't appear
// in the body and therefore don't overwrite stored values on the
// merge. Errors here are theoretical (the inputs are plain strings),
// but we return the wrapped error to keep the call site explicit.
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

// gunzipBytes returns the gzip-decompressed contents of b. A corrupt
// header or truncated stream returns an error; the caller turns that
// into a NAK so JetStream retries — a transient publisher hiccup
// should not silently drop the batch.
func gunzipBytes(b []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	return io.ReadAll(gr)
}

// SpotlightOrgIndex serves three roles in the consumer: unmarshal
// target for the wire-side row, document body on ES write, and source
// of truth for the ES mapping via esPropertiesFromStruct. Every field
// is `omitempty` `string` so absent values serialize away and
// doc-merge upsert preserves the stored value rather than overwriting
// with empty. Fields not in this struct are silently ignored by the
// json decoder — hr-syncer is free to publish additional fields.
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
