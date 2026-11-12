package services

import (
	"context"
	"yanfachatbi/internal/constants"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/vectordb"
	"sort"
	"strconv"
	"strings"
)

func (i *objectIndex) Search(ctx context.Context, s schemaobjects.Snapshot, kb, query string, limit int) ([]vectordb.SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var err error
	s, err = checkedObjectSnapshot(s)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = constants.DefaultTopK
	}
	if limit > 50 {
		limit = 50
	}
	if len(s.Objects) == 0 {
		return []vectordb.SearchResult{}, nil
	}
	fallback := func() ([]vectordb.SearchResult, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return objectStructureFallback(s, kb, query, limit), nil
	}
	state, err := i.state.Read(ctx, s.Scope)
	if err != nil || !registryMatches(state, s) || !manifestMatches(state.Manifest, s, kb) || state.Manifest.ActiveGeneration == "" {
		return fallback()
	}
	generation := state.Manifest.ActiveGeneration
	vector, err := i.embedding.EmbedQuery(ctx, query)
	if err != nil || !validObjectVector(vector, i.embedding.GetDimension()) {
		return fallback()
	}
	filter := objectScopeFilter(s.Scope, generation)
	request := vectordb.HybridSearchRequest{Vector: vector, Filter: filter, TopK: limit * constants.SchemaSearchOverfetch, ScoreThreshold: float32(constants.DefaultScoreThreshold), TextQuery: query, TextField: "content",
		ExtraTextLegs: []vectordb.TextSearchLeg{{Field: "table_name", Query: query}}}
	hits, err := i.vectors.HybridSearch(ctx, request)
	if err != nil {
		hits, err = i.vectors.Search(ctx, vectordb.SearchRequest{Vector: vector, Filter: filter, TopK: limit * constants.SchemaSearchOverfetch, ScoreThreshold: float32(constants.DefaultScoreThreshold)})
		if err != nil {
			return fallback()
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	// Do not return the retired generation if publication raced the query.
	current, err := i.state.Read(ctx, s.Scope)
	if err != nil || !registryMatches(current, s) || !manifestMatches(current.Manifest, s, kb) || current.Manifest.ActiveGeneration != generation {
		return fallback()
	}
	objects := map[string]schemaobjects.Object{}
	for _, o := range s.Objects {
		objects[o.Metadata.ObjectID] = o
	}
	safe := []vectordb.SearchResult{}
	degraded := map[string]bool{}
	for _, hit := range hits {
		p := hit.Payload
		scopeOK := true
		for k, v := range filter {
			if p[k] != v {
				scopeOK = false
				break
			}
		}
		if !scopeOK {
			continue
		}
		id, _ := p["object_id"].(string)
		o, ok := objects[id]
		if !ok {
			continue
		}
		if !currentObjectPayload(hit, s, kb, generation, o) {
			if !degraded[id] {
				safe = append(safe, currentObjectSummary(s, kb, o, hit.Score))
				degraded[id] = true
			}
			continue
		}
		safe = append(safe, hit)
	}
	// Once one part is stale, never mix its fallback summary with other old parts.
	filtered := safe[:0]
	for _, hit := range safe {
		id, _ := hit.Payload["object_id"].(string)
		if !degraded[id] || hit.Payload["needs_rebuild"] == true {
			filtered = append(filtered, hit)
		}
	}
	return groupObjectResults(filtered, limit), nil
}
func currentObjectPayload(hit vectordb.SearchResult, s schemaobjects.Snapshot, kb, generation string, o schemaobjects.Object) bool {
	p := hit.Payload
	// Unknown continuity never authorizes reuse of stored text.
	if o.Metadata.InstanceVersion == "" {
		return false
	}
	if p["object_instance_version"] != o.Metadata.InstanceVersion || p["schema_fingerprint"] != o.Metadata.Fingerprint ||
		p["snapshot_fingerprint"] != s.Fingerprint || p["kb_revision"] != kb || p["object_type"] != string(o.Metadata.Ref.Kind) ||
		p["schema_name"] != o.Metadata.Ref.Schema || p["object_name"] != o.Metadata.Ref.Name ||
		p["table_name"] != schemaobjects.SQLIdentifier(o.Metadata.Ref) || p["sql_identifier"] != schemaobjects.SQLIdentifier(o.Metadata.Ref) {
		return false
	}
	partText, _ := p["part_index"].(string)
	part, err := strconv.Atoi(partText)
	if err != nil || part < 1 {
		return false
	}
	schemaParts := (len(o.Columns) + objectColumnsPerChunk - 1) / objectColumnsPerChunk
	total := schemaParts
	if len(o.ForeignKeys) > 0 {
		total++
	}
	if part > total || p["total_parts"] != strconv.Itoa(total) {
		return false
	}
	kind := "schema"
	if part > schemaParts {
		kind = "relationship"
	}
	if p["type"] != kind || hit.ID != objectPointID(s.Scope, generation, o, partText) {
		return false
	}
	content, ok := p["content"].(string)
	return ok && content != "" && p["content_digest"] == objectContentDigest(content)
}
func currentObjectSummary(s schemaobjects.Snapshot, kb string, o schemaobjects.Object, score float32) vectordb.SearchResult {
	c := SchemaChunk{Content: schemaobjects.Format([]schemaobjects.Object{o}), Metadata: map[string]string{"part_index": "0", "total_parts": "1", "type": "schema"}}
	p := objectChunkPayload(s, kb, "", o, c)
	p["needs_rebuild"] = true
	return vectordb.SearchResult{ID: "current:" + o.Metadata.ObjectID, Score: score, Payload: p}
}
func objectStructureFallback(s schemaobjects.Snapshot, kb, query string, limit int) []vectordb.SearchResult {
	hits := []vectordb.SearchResult{}
	q := strings.ToLower(query)
	for _, o := range s.Objects {
		score := float32(0)
		if q != "" && strings.Contains(q, strings.ToLower(o.Metadata.Ref.Name)) {
			score = 1
		}
		hits = append(hits, currentObjectSummary(s, kb, o, score))
	}
	return groupObjectResults(hits, limit)
}

// Group by object identity while retaining the legacy score pruning, per-object
// chunk budget and round-robin diversity. Only a proven zero row count is empty;
// unknown counts compete on relevance alongside nonempty objects.
func groupObjectResults(hits []vectordb.SearchResult, limit int) []vectordb.SearchResult {
	if limit <= 0 {
		return []vectordb.SearchResult{}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].Score > hits[b].Score })
	type objectGroup struct {
		chunks []vectordb.SearchResult
		score  float32
		empty  bool
	}
	groups := map[string]*objectGroup{}
	order := []string{}
	seen := map[string]bool{}
	for _, hit := range hits {
		id, _ := hit.Payload["object_id"].(string)
		if id == "" || seen[hit.ID] {
			continue
		}
		seen[hit.ID] = true
		group, exists := groups[id]
		if !exists {
			group = &objectGroup{score: hit.Score, empty: knownEmptyObject(hit.Payload)}
			groups[id] = group
			order = append(order, id)
		}
		if len(group.chunks) < constants.MaxChunksPerTable {
			group.chunks = append(group.chunks, hit)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := groups[order[a]], groups[order[b]]
		if x.empty != y.empty {
			return !x.empty
		}
		return x.score > y.score
	})
	if len(order) > constants.MinSchemaResults {
		minScore := groups[order[0]].score * float32(constants.MinRelativeScore)
		for n := constants.MinSchemaResults; n < len(order); n++ {
			current, previous := groups[order[n]].score, groups[order[n-1]].score
			if current < minScore || (previous > 0 && current/previous < float32(constants.ScoreCliffRatio)) {
				order = order[:n]
				break
			}
		}
	}
	out := []vectordb.SearchResult{}
	for round := 0; round < constants.MaxChunksPerTable; round++ {
		for _, id := range order {
			parts := groups[id].chunks
			if round < len(parts) {
				out = append(out, parts[round])
				if len(out) == limit {
					return out
				}
			}
		}
	}
	return out
}
func knownEmptyObject(payload map[string]interface{}) bool {
	if payload["row_count_status"] != "known" {
		return false
	}
	switch count := payload["row_count"].(type) {
	case int:
		return count == 0
	case int64:
		return count == 0
	case float64:
		return count == 0
	default:
		return false
	}
}
