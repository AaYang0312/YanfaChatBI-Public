package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

// The real provider and SDK run against loopback only. These tests catch a
// wrong model/dimension in the actual request, official-endpoint fallback,
// lost query forwarding, and batches that exceed the compatible API limit.
func TestQwenFlashCompatibleRequests(t *testing.T) {
	const modelID = "qwen3.7-text-embedding-flash"
	for _, tc := range []struct {
		name  string
		count int
	}{
		{"single", 1}, {"query", 1}, {"batch", 21},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var observed []string
			var batches []int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method != http.MethodPost || r.URL.Path != "/compatible-mode/v1/embeddings" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				var request struct {
					Model      string   `json:"model"`
					Dimensions int      `json:"dimensions"`
					Input      []string `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid JSON", http.StatusBadRequest)
					return
				}
				if request.Model != modelID || request.Dimensions != 1024 {
					t.Errorf("request model/dimension = %q/%d, want exact Flash/1024", request.Model, request.Dimensions)
				}
				if len(request.Input) == 0 || len(request.Input) > 20 {
					t.Errorf("invalid batch size: %d", len(request.Input))
				}
				batches = append(batches, len(request.Input))
				data := make([]map[string]interface{}, len(request.Input))
				for i := range request.Input {
					vector := make([]float32, 1024)
					vector[0] = float32(len(observed) + i + 1)
					data[i] = map[string]interface{}{"object": "embedding", "index": i, "embedding": vector}
				}
				observed = append(observed, request.Input...)
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]interface{}{
					"object": "list", "model": modelID, "data": data,
					"usage": map[string]int{"prompt_tokens": len(request.Input), "total_tokens": len(request.Input)},
				}); err != nil {
					t.Errorf("encode response: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			provider, err := NewProvider(Config{Provider: "openai", Model: modelID, APIKey: "synthetic-test-only", BaseURL: server.URL + "/compatible-mode/v1"})
			if err != nil {
				t.Fatal(err)
			}
			inputs := make([]string, tc.count)
			for i := range inputs {
				inputs[i] = fmt.Sprintf("synthetic document %02d", i+1)
			}
			var vectors [][]float32
			switch tc.name {
			case "single":
				var vector []float32
				vector, err = provider.Embed(context.Background(), inputs[0])
				vectors = [][]float32{vector}
			case "query":
				var vector []float32
				vector, err = provider.EmbedQuery(context.Background(), inputs[0])
				vectors = [][]float32{vector}
			case "batch":
				vectors, err = provider.EmbedBatch(context.Background(), inputs)
			}
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(observed, inputs) {
				t.Fatalf("input delivery changed: got %v, want %v", observed, inputs)
			}
			wantBatches := []int{1}
			if tc.name == "batch" {
				wantBatches = []int{20, 1}
			}
			if !reflect.DeepEqual(batches, wantBatches) {
				t.Fatalf("batches = %v, want %v", batches, wantBatches)
			}
			if len(vectors) != tc.count {
				t.Fatalf("returned %d vectors, want %d", len(vectors), tc.count)
			}
			for i, vector := range vectors {
				if len(vector) != 1024 || vector[0] != float32(i+1) {
					t.Fatalf("returned vector %d has wrong length/order", i)
				}
			}
		})
	}
}
