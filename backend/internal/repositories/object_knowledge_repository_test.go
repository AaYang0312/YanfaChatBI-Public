package repositories

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"yanfachatbi/internal/models"
	"reflect"
	"testing"
	"time"
)

type kbWriteRecorder struct {
	filter, update any
	opts           []*options.UpdateOptions
	inserted       any
	writes         int
	matched        int64
	err            error
}

func (r *kbWriteRecorder) UpdateOne(_ context.Context, f, u any, o ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	r.writes++
	r.filter = f
	r.update = u
	r.opts = o
	return &mongo.UpdateResult{MatchedCount: r.matched}, r.err
}
func (r *kbWriteRecorder) InsertOne(_ context.Context, d any, _ ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	r.writes++
	r.inserted = d
	return &mongo.InsertOneResult{}, r.err
}
func repositoryKB() *models.KnowledgeBase {
	return &models.KnowledgeBase{ChatID: primitive.NewObjectID(), UserID: primitive.NewObjectID(), StorageRevision: 7, KnowledgeVersion: 2, Base: models.NewBase()}
}
func TestObjectKnowledgeRepositoryCASBoundary(t *testing.T) {
	previous := repositoryKB()
	next := *previous
	next.StorageRevision = 8
	next.KBRevision = "derived"
	next.TableDescriptions = []models.TableDescription{{TableName: "history", Description: "original"}}
	recorder := &kbWriteRecorder{matched: 1}
	ensured := 0
	ok, err := writeObjectKnowledgeCAS(context.Background(), recorder, func(context.Context) error { ensured++; return nil }, previous, &next)
	if err != nil || !ok || ensured != 1 || recorder.writes != 1 {
		t.Fatalf("CAS not written: %v %v %+v", ok, err, recorder)
	}
	f := recorder.filter.(bson.M)
	if f["_id"] != previous.ID || f["chat_id"] != previous.ChatID || f["user_id"] != previous.UserID || f["storage_revision"] != int64(7) {
		t.Fatalf("missing storage CAS boundary: %+v", f)
	}
	set := recorder.update.(bson.M)["$set"].(bson.M)
	if set["storage_revision"] != int64(8) || set["knowledge_version"] != 2 || set["kb_revision"] != "derived" || !reflect.DeepEqual(set["table_descriptions"], next.TableDescriptions) {
		t.Fatalf("bad persistent fields: %+v", set)
	}
	if len(recorder.opts) > 0 && recorder.opts[0].Upsert != nil && *recorder.opts[0].Upsert {
		t.Fatal("existing CAS must never upsert")
	}
	recorder.matched = 0
	if ok, err = writeObjectKnowledgeCAS(context.Background(), recorder, func(context.Context) error { return nil }, previous, &next); err != nil || ok {
		t.Fatal("stale CAS accepted")
	}
}
func TestObjectKnowledgeRepositoryConcurrentCreateAndLegacyPromotion(t *testing.T) {
	next := repositoryKB()
	next.StorageRevision = 1
	recorder := &kbWriteRecorder{err: mongo.WriteException{WriteErrors: []mongo.WriteError{{Code: 11000}}}}
	ok, err := writeObjectKnowledgeCAS(context.Background(), recorder, func(context.Context) error { return nil }, nil, next)
	if err != nil || ok || recorder.inserted == nil {
		t.Fatalf("duplicate chat create not conflict: %v %v", ok, err)
	}
	previous := *next
	previous.StorageRevision = 0
	previous.KnowledgeVersion = 0
	previous.UserID = primitive.NilObjectID
	recorder = &kbWriteRecorder{matched: 1}
	ok, err = writeObjectKnowledgeCAS(context.Background(), recorder, func(context.Context) error { return nil }, &previous, next)
	if err != nil || !ok {
		t.Fatal("legal ownerless legacy promotion failed")
	}
	filter := recorder.filter.(bson.M)
	if !reflect.DeepEqual(filter["storage_revision"], bson.M{"$in": bson.A{nil, int64(0)}}) || !reflect.DeepEqual(filter["user_id"], bson.M{"$in": bson.A{nil, primitive.NilObjectID}}) {
		t.Fatalf("legacy zero/absent evidence wrong: %+v", filter)
	}
	recorder = &kbWriteRecorder{}
	if ok, err = writeObjectKnowledgeCAS(context.Background(), recorder, func(context.Context) error { return errors.New("index unavailable") }, nil, next); err == nil || ok || recorder.writes != 0 {
		t.Fatal("create without unique index guarantee")
	}
}
func TestObjectKnowledgeRepositoryLegacyWriterCannotOverwriteV2(t *testing.T) {
	kb := repositoryKB()
	recorder := &kbWriteRecorder{matched: 1}
	if err := writeLegacyKnowledge(context.Background(), recorder, func(context.Context) error { return nil }, kb, time.Now()); err == nil || recorder.writes != 0 {
		t.Fatal("v2 document accepted by legacy writer")
	}
	kb.KnowledgeVersion = 0
	recorder = &kbWriteRecorder{err: mongo.WriteException{WriteErrors: []mongo.WriteError{{Code: 11000}}}}
	if err := writeLegacyKnowledge(context.Background(), recorder, func(context.Context) error { return nil }, kb, time.Now()); err == nil {
		t.Fatal("late legacy upsert ignored conflict")
	}
	filter := recorder.filter.(bson.M)
	if !reflect.DeepEqual(filter["knowledge_version"], bson.M{"$ne": 2}) {
		t.Fatalf("legacy writer lacks v2 guard: %+v", filter)
	}
	update := recorder.update.(bson.M)
	if update["$inc"].(bson.M)["storage_revision"] != int64(1) {
		t.Fatal("legacy write does not advance CAS version")
	}
	if update["$set"].(bson.M)["user_id"] != kb.UserID {
		t.Fatal("legacy owner not persisted")
	}
}
