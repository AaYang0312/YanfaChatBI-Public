package services

import (
	"context"
	"yanfachatbi/internal/constants"
	"yanfachatbi/pkg/vectordb"
	"testing"
)

func TestObjectIndexSearchRetainsLegacyBudget(t *testing.T) {
	s := indexSnapshot(t, 55)
	i, _, v, _ := indexFixture(t, s)
	if err := i.Build(context.Background(), s, "kb-1", chunksFor(t, s)); err != nil {
		t.Fatal(err)
	}
	v.failHybrid = true
	hits, err := i.Search(context.Background(), s, "kb-1", "Sales", 1)
	if err != nil || len(hits) != 1 {
		t.Fatalf("limit must bound result chunks: %d %v", len(hits), err)
	}
	if v.hybrid.ScoreThreshold != float32(constants.DefaultScoreThreshold) || v.search.ScoreThreshold != v.hybrid.ScoreThreshold {
		t.Fatal("semantic/fallback threshold changed")
	}
	_, err = i.Search(context.Background(), s, "kb-1", "Sales", 0)
	if err != nil {
		t.Fatal(err)
	}
	if v.hybrid.TopK != constants.DefaultTopK*constants.SchemaSearchOverfetch {
		t.Fatalf("default retrieval budget changed: %d", v.hybrid.TopK)
	}
}
func TestObjectIndexDiversityAndScorePruning(t *testing.T) {
	hit := func(id, object string, score float32) vectordb.SearchResult {
		return vectordb.SearchResult{ID: id, Score: score, Payload: map[string]interface{}{"object_id": object, "row_count_status": "unknown"}}
	}
	hits := []vectordb.SearchResult{hit("a1", "a", 0.9), hit("a2", "a", 0.8), hit("a3", "a", 0.75), hit("b1", "b", 0.7), hit("noise1", "noise1", 0.03), hit("noise2", "noise2", 0.02)}
	for _, limit := range []int{3, 10} {
		got := groupObjectResults(hits, limit)
		if len(got) != 3 || got[0].ID != "a1" || got[1].ID != "b1" || got[2].ID != "a2" {
			t.Fatalf("lost diversity/chunk cap/score pruning: %+v", got)
		}
	}
}
