package models_test

import (
	"encoding/json"
	"github.com/gin-gonic/gin/binding"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
)

func TestObjectKnowledgeWirePreservesEvidence(t *testing.T) {
	raw := []byte(`{"kb_revision":"revision-1","table_descriptions":[{"table_name":"legacy.name","description":" 人工原文 ","object_id":"object-1","object_instance_version":"instance-1","provenance":"user_edit","state":"mapped","updated_at":"2026-11-11T01:00:00Z","object_scope":{"owner_id":"owner","chat_id":"chat","source_key":"source","binding_revision":2,"connection_revision":3,"selection_revision":4},"field_descriptions":[{"field_name":"amount","description":"金额","provenance":"ai_generated","state":"pending","updated_at":"2026-11-11T02:00:00Z"}]}]}`)
	var kb models.KnowledgeBase
	if err := json.Unmarshal(raw, &kb); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "bson"} {
		t.Run(format, func(t *testing.T) {
			roundtrip := kb
			if format == "bson" {
				data, err := bson.Marshal(kb)
				if err != nil {
					t.Fatal(err)
				}
				roundtrip = models.KnowledgeBase{}
				if err := bson.Unmarshal(data, &roundtrip); err != nil {
					t.Fatal(err)
				}
			}
			data, err := json.Marshal(roundtrip)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got["kb_revision"] != "revision-1" {
				t.Errorf("KB revision lost: %v", got["kb_revision"])
			}
			row := got["table_descriptions"].([]any)[0].(map[string]any)
			for key, want := range map[string]string{"table_name": "legacy.name", "description": " 人工原文 ", "object_id": "object-1", "object_instance_version": "instance-1", "provenance": "user_edit", "state": "mapped", "updated_at": "2026-11-11T01:00:00Z"} {
				if row[key] != want {
					t.Errorf("%s: got %v, want %s", key, row[key], want)
				}
			}
			if scope, ok := row["object_scope"].(map[string]any); !ok || scope["source_key"] != "source" {
				t.Errorf("scope evidence lost: %v", row["object_scope"])
			}
			field := row["field_descriptions"].([]any)[0].(map[string]any)
			for key, want := range map[string]string{"field_name": "amount", "description": "金额", "provenance": "ai_generated", "state": "pending", "updated_at": "2026-11-11T02:00:00Z"} {
				if field[key] != want {
					t.Errorf("field %s: got %v, want %s", key, field[key], want)
				}
			}
		})
	}
}

func TestObjectKnowledgeLegacyWireAndDTORevision(t *testing.T) {
	var kb models.KnowledgeBase
	if err := json.Unmarshal([]byte(`{"table_descriptions":[{"table_name":"old","description":"原文","field_descriptions":[{"field_name":"x","description":"旧列"}]}]}`), &kb); err != nil {
		t.Fatal(err)
	}
	if row := kb.GetTableDescription("old"); row == nil || row.Description != "原文" || row.FieldDescriptions[0].Description != "旧列" {
		t.Fatalf("legacy text lost: %+v", kb)
	}
	var response dtos.KnowledgeBaseResponse
	if err := json.Unmarshal([]byte(`{"kb_revision":"effective-version"}`), &response); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(response)
	var output map[string]any
	_ = json.Unmarshal(data, &output)
	if output["kb_revision"] != "effective-version" {
		t.Errorf("response omits current revision: %s", data)
	}
	var request dtos.UpdateKnowledgeBaseRequest
	if err := json.Unmarshal([]byte(`{"table_descriptions":[],"expected_kb_revision":"expected-version"}`), &request); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(request)
	_ = json.Unmarshal(data, &output)
	if output["expected_kb_revision"] != "expected-version" {
		t.Errorf("request loses expected revision: %s", data)
	}
}

func TestObjectKnowledgeConfirmationRequestOnlyCarriesMappingIntent(t *testing.T) {
	var request dtos.ConfirmKnowledgeBaseMappingRequest
	if err := json.Unmarshal([]byte(`{"description_index":0,"object_id":"target","expected_kb_revision":"revision","object_instance_version":"forged","object_scope":{"source_key":"forged"},"provenance":"user_edit","description":"replacement"}`), &request); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["description_index"] != float64(0) || wire["object_id"] != "target" || wire["expected_kb_revision"] != "revision" {
		t.Fatalf("mapping intent lost: %s", data)
	}
	for _, key := range []string{"object_instance_version", "object_scope", "provenance", "description"} {
		if _, ok := wire[key]; ok {
			t.Errorf("client can supply server evidence %s", key)
		}
	}
	if err := binding.Validator.ValidateStruct(request); err != nil {
		t.Fatalf("index zero must be accepted: %v", err)
	}
	for _, raw := range []string{`{}`, `{"description_index":-1,"object_id":"target","expected_kb_revision":"revision"}`, `{"description_index":0,"object_id":"target"}`} {
		var invalid dtos.ConfirmKnowledgeBaseMappingRequest
		if err := json.Unmarshal([]byte(raw), &invalid); err != nil {
			t.Fatal(err)
		}
		if err := binding.Validator.ValidateStruct(invalid); err == nil {
			t.Errorf("invalid confirmation accepted: %s", raw)
		}
	}
}
