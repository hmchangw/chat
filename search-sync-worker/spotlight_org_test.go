package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
)

func TestSpotlightOrgCollection_Metadata(t *testing.T) {
	coll := newSpotlightOrgCollection("spotlightorg-site-a", false)

	assert.Equal(t, "spotlight-org-sync", coll.ConsumerName())
	assert.Equal(t, "spotlight_org_template", coll.TemplateName())

	cfg := coll.StreamConfig("site-a")
	assert.Equal(t, "HR_SYNC_site-a", cfg.Name)
	assert.Equal(t, []string{"hr.sync.site-a.>"}, cfg.Subjects)
	assert.Empty(t, cfg.Sources)

	filters := coll.FilterSubjects("site-a")
	assert.Equal(t, []string{"hr.sync.site-a.employees.upsert"}, filters)
}

func TestSpotlightOrgTemplateBody_Prod(t *testing.T) {
	coll := newSpotlightOrgCollection("spotlightorg-site-a", false)
	body := coll.TemplateBody()
	require.NotNil(t, body)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	patterns := parsed["index_patterns"].([]any)
	assert.Equal(t, "spotlightorg-site-a", patterns[0])

	tmpl := parsed["template"].(map[string]any)
	idx := tmpl["settings"].(map[string]any)["index"].(map[string]any)
	assert.Equal(t, float64(3), idx["number_of_shards"])
	assert.Equal(t, float64(1), idx["number_of_replicas"])

	mappings := tmpl["mappings"].(map[string]any)
	assert.Equal(t, false, mappings["dynamic"])
	props := mappings["properties"].(map[string]any)
	for _, f := range []string{
		"sectId", "sectTCName", "sectName", "sectDescription",
		"deptId", "deptTCName", "deptName", "deptDescription", "divisionId",
	} {
		field, ok := props[f].(map[string]any)
		require.True(t, ok, "missing property: %s", f)
		assert.Equal(t, "search_as_you_type", field["type"], "field %s wrong type", f)
		assert.Equal(t, "custom_analyzer", field["analyzer"], "field %s wrong analyzer", f)
	}
}

func TestSpotlightOrgTemplateBody_Dev(t *testing.T) {
	coll := newSpotlightOrgCollection("spotlightorg-site-a", true)
	body := coll.TemplateBody()
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	idx := parsed["template"].(map[string]any)["settings"].(map[string]any)["index"].(map[string]any)
	assert.Equal(t, float64(1), idx["number_of_shards"])
	assert.Equal(t, float64(0), idx["number_of_replicas"])
}

// TestSpotlightOrgTemplateProperties_MatchesStruct guards against
// silent drift between SpotlightOrgIndex (the wire row + ES doc
// projection + mapping source) and the properties block written
// into the ES template. If a future edit adds an `es`-tagged field
// to the struct or forgets to add one, this test fails. Mirrors
// TestSpotlightTemplateProperties_MatchesStruct in spotlight_test.go.
func TestSpotlightOrgTemplateProperties_MatchesStruct(t *testing.T) {
	props := esPropertiesFromStruct[SpotlightOrgIndex]()

	typ := reflect.TypeOf(SpotlightOrgIndex{})
	esFieldCount := 0
	for i := range typ.NumField() {
		field := typ.Field(i)
		esTag := field.Tag.Get("es")
		if esTag == "" || esTag == "-" {
			continue
		}
		esFieldCount++
		jsonTag := field.Tag.Get("json")
		name, _, _ := strings.Cut(jsonTag, ",")
		_, ok := props[name]
		assert.True(t, ok, "template missing property for field %s (json %s)", field.Name, name)
	}
	assert.Equal(t, esFieldCount, len(props))
	assert.Equal(t, 9, esFieldCount, "SpotlightOrgIndex should expose exactly 9 ES-mapped fields")
}

// makeHRSyncEvent builds a plaintext (gzip=false) HRSyncEvent containing
// the given employees. Used by every BuildAction test that doesn't
// exercise the compression path.
func makeHRSyncEvent(t *testing.T, ts int64, employees []SpotlightOrgIndex) []byte {
	t.Helper()
	payload, err := json.Marshal(employees)
	require.NoError(t, err)
	evt := model.HRSyncEvent{
		Timestamp: ts,
		BatchID:   "b-1",
		Gzip:      false,
		Payload:   payload,
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	return data
}

func TestSpotlightOrg_BuildAction_HappyPath(t *testing.T) {
	coll := newSpotlightOrgCollection("spotlightorg-site-a", false)
	data := makeHRSyncEvent(t, 1735689600000, []SpotlightOrgIndex{
		{SectID: "S1", SectName: "Eng", DeptID: "D1", DeptName: "Tech"},
		{SectID: "S2", SectName: "Sales", DeptID: "D2", DeptName: "Biz"},
	})

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 2)

	docIDs := map[string]bool{}
	for _, a := range actions {
		assert.Equal(t, searchengine.ActionUpdate, a.Action)
		assert.Equal(t, "spotlightorg-site-a", a.Index)
		assert.Equal(t, int64(0), a.Version, "ActionUpdate must not use external versioning")
		docIDs[a.DocID] = true

		var body map[string]any
		require.NoError(t, json.Unmarshal(a.Doc, &body))
		assert.Equal(t, true, body["doc_as_upsert"])
		assert.Contains(t, body, "doc")
	}
	assert.True(t, docIDs["S1"])
	assert.True(t, docIDs["S2"])
}

// TestSpotlightOrg_BuildAction_DedupBySectID covers many-employees-one-section:
// multiple employees sharing the same sectId must collapse to a single ES
// action. The kept row is the LAST occurrence so the most recent in-batch
// update wins.
func TestSpotlightOrg_BuildAction_DedupBySectID(t *testing.T) {
	coll := newSpotlightOrgCollection("spotlightorg-site-a", false)
	data := makeHRSyncEvent(t, 1, []SpotlightOrgIndex{
		{SectID: "S1", SectName: "Engineering"},
		{SectID: "S1", SectName: "Engineering Renamed"},
		{SectID: "S2", SectName: "Sales"},
	})

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 2)

	var s1Body map[string]any
	for _, a := range actions {
		if a.DocID == "S1" {
			require.NoError(t, json.Unmarshal(a.Doc, &s1Body))
		}
	}
	require.NotNil(t, s1Body)
	doc := s1Body["doc"].(map[string]any)
	assert.Equal(t, "Engineering Renamed", doc["sectName"], "last-wins on dedup")
}

func TestSpotlightOrg_BuildAction_EmptySectIDsSkipped(t *testing.T) {
	coll := newSpotlightOrgCollection("spotlightorg-site-a", false)
	data := makeHRSyncEvent(t, 1, []SpotlightOrgIndex{
		{SectID: "", SectName: "no-section"},
		{SectID: "S1", SectName: "Eng"},
		{SectID: "", DeptID: "D9"},
	})

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "S1", actions[0].DocID)
}

func TestSpotlightOrg_BuildAction_AllEmptySectIDs(t *testing.T) {
	coll := newSpotlightOrgCollection("spotlightorg-site-a", false)
	data := makeHRSyncEvent(t, 1, []SpotlightOrgIndex{
		{SectName: "no-section-1"},
		{SectName: "no-section-2"},
	})

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	assert.Nil(t, actions, "all empty sectIds → zero actions, handler acks JS msg without ES write")
}

func TestSpotlightOrg_BuildAction_EmptyEmployees(t *testing.T) {
	coll := newSpotlightOrgCollection("spotlightorg-site-a", false)
	data := makeHRSyncEvent(t, 1, []SpotlightOrgIndex{})

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	assert.Nil(t, actions)
}

// makeHRSyncEventGzip mirrors makeHRSyncEvent but gzip-compresses the
// employee payload. Used to exercise the consumer's decompression path.
func makeHRSyncEventGzip(t *testing.T, ts int64, employees []SpotlightOrgIndex) []byte {
	t.Helper()
	raw, err := json.Marshal(employees)
	require.NoError(t, err)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, err = gz.Write(raw)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	// Gzip payloads are carried as base64-encoded JSON strings so they
	// survive JSON marshaling of the envelope. json.Marshal([]byte)
	// automatically base64-encodes, matching what BuildAction expects.
	encodedPayload, err := json.Marshal(buf.Bytes())
	require.NoError(t, err)
	evt := model.HRSyncEvent{
		Timestamp: ts,
		BatchID:   "b-1",
		Gzip:      true,
		Payload:   json.RawMessage(encodedPayload),
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	return data
}

func TestSpotlightOrg_BuildAction_Gzip(t *testing.T) {
	coll := newSpotlightOrgCollection("spotlightorg-site-a", false)
	data := makeHRSyncEventGzip(t, 1, []SpotlightOrgIndex{
		{SectID: "S1", SectName: "Engineering"},
	})

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "S1", actions[0].DocID)

	var body map[string]any
	require.NoError(t, json.Unmarshal(actions[0].Doc, &body))
	doc := body["doc"].(map[string]any)
	assert.Equal(t, "Engineering", doc["sectName"])
}

// TestSpotlightOrg_BuildAction_PartialFields locks in the partial-update
// contract: a row carrying only SectID + SectName must produce an ES `doc`
// body containing ONLY those two keys. Other org fields must be absent so
// doc-merge preserves their stored values.
func TestSpotlightOrg_BuildAction_PartialFields(t *testing.T) {
	coll := newSpotlightOrgCollection("spotlightorg-site-a", false)
	data := makeHRSyncEvent(t, 1, []SpotlightOrgIndex{
		{SectID: "S1", SectName: "Engineering"},
	})

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 1)

	var body map[string]any
	require.NoError(t, json.Unmarshal(actions[0].Doc, &body))
	doc := body["doc"].(map[string]any)

	assert.Equal(t, "S1", doc["sectId"])
	assert.Equal(t, "Engineering", doc["sectName"])
	for _, absent := range []string{
		"sectTCName", "sectDescription",
		"deptId", "deptTCName", "deptName", "deptDescription", "divisionId",
	} {
		_, present := doc[absent]
		assert.False(t, present, "doc must not carry %s when input did not set it", absent)
	}
}

func TestSpotlightOrg_BuildAction_Errors(t *testing.T) {
	coll := newSpotlightOrgCollection("spotlightorg-site-a", false)

	t.Run("malformed envelope", func(t *testing.T) {
		_, err := coll.BuildAction([]byte("{invalid"))
		assert.Error(t, err)
	})

	t.Run("missing timestamp", func(t *testing.T) {
		data := makeHRSyncEvent(t, 0, []SpotlightOrgIndex{{SectID: "S1"}})
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("malformed employees payload", func(t *testing.T) {
		evt := model.HRSyncEvent{
			Timestamp: 1,
			Payload:   json.RawMessage(`not json`),
		}
		data, _ := json.Marshal(evt)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("corrupt gzip header", func(t *testing.T) {
		// Encode "not gzip" bytes as a base64 JSON string so the envelope
		// marshals cleanly. BuildAction will decode it, then fail when
		// gzip.NewReader sees no valid gzip magic bytes.
		corruptGzip, _ := json.Marshal([]byte("not gzip"))
		evt := model.HRSyncEvent{
			Timestamp: 1,
			Gzip:      true,
			Payload:   json.RawMessage(corruptGzip),
		}
		data, _ := json.Marshal(evt)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("gzip payload not a base64 string", func(t *testing.T) {
		// Payload must be a JSON string (base64) when Gzip=true.
		// If it's a JSON number or object, the base64 decode step fails.
		evt := model.HRSyncEvent{
			Timestamp: 1,
			Gzip:      true,
			Payload:   json.RawMessage(`123`),
		}
		data, _ := json.Marshal(evt)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})
}
