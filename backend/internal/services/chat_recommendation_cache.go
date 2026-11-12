package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"time"
)

// NewCachedQueryRecommendations binds stored suggestions to the server-owned
// chat snapshot. Unversioned cache entries cannot establish current scope.
func NewCachedQueryRecommendations(chat *models.Chat, recommendations []dtos.CachedQueryRecommendation) dtos.CachedQueryRecommendations {
	return dtos.CachedQueryRecommendations{Recommendations: recommendations, CreatedAt: time.Now().Unix(), Scope: recommendationCacheScope(chat)}
}

func recommendationCacheScope(chat *models.Chat) string {
	if chat == nil || chat.ID.IsZero() || chat.UserID.IsZero() || chat.SelectedObjects != nil {
		return ""
	}
	// Connection is the persisted encrypted value. Never return it or put it in
	// a cache payload: only the digest leaves this function. Rebinding, narrowed
	// selection, schema refresh, and mode changes all invalidate prior evidence.
	value := struct {
		Owner, Chat, Source, Fingerprint, Selection string
		Binding, Connection, Revision               int64
		Schema                                      models.Connection
		Settings                                    models.ChatSettings
	}{chat.UserID.Hex(), chat.ID.Hex(), chat.SourceID, chat.SourceConnectionFingerprint, chat.SelectedCollections,
		chat.SourceBindingRevision, chat.ConnectionRevision, chat.SelectionRevision, chat.Connection, chat.Settings}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
