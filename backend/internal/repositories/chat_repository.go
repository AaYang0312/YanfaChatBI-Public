package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/mongodb"
	"yanfachatbi/pkg/redis"
	"yanfachatbi/pkg/sourcegate"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

type ChatRepository interface {
	Create(chat *models.Chat) error
	ReadCurrent(ctx context.Context, owner, id primitive.ObjectID) (*models.Chat, error)
	MarkPendingCAS(ctx context.Context, previous, pending *models.Chat) error
	CommitBindingCAS(ctx context.Context, pending, confirmed *models.Chat) error
	PatchMetadata(ctx context.Context, owner, id primitive.ObjectID, patch ChatMetadataPatch) error
	UpdateIndependentConnectionCAS(ctx context.Context, previous *models.Chat, connection models.Connection) error
	UpdateSpreadsheetNameCAS(ctx context.Context, previous *models.Chat, name string) error
	UpdateConnectionSchema(ctx context.Context, origin *models.Chat, schema string) error
	UpdateChatTimestamp(chatID primitive.ObjectID) error
	Delete(id primitive.ObjectID) error
	FindByID(id primitive.ObjectID) (*models.Chat, error)
	FindByUserID(userID primitive.ObjectID, page, pageSize int) ([]*models.Chat, int64, error)
	CreateMessage(message *models.Message) error
	UpdateMessage(id primitive.ObjectID, message *models.Message) error
	DeleteMessages(chatID primitive.ObjectID) error
	FindMessagesByChat(chatID primitive.ObjectID, page, pageSize int) ([]*models.Message, int64, error)
	FindLatestMessageByChat(chatID primitive.ObjectID, page, pageSize int) ([]*models.Message, int64, error)
	FindMessageByID(id primitive.ObjectID) (*models.Message, error)
	FindNextMessageByID(id primitive.ObjectID) (*models.Message, error)
	FindPinnedMessagesByChat(chatID primitive.ObjectID) ([]models.Message, error)
	FindMessagesByChatAfterTime(chatID primitive.ObjectID, after time.Time, page, pageSize int) ([]models.Message, int64, error)
	UpdateQueryVisualizationID(messageID, queryID, visualizationID primitive.ObjectID) error
}

// concrete implementation of ChatRepository, using interface composition
type chatRepository struct {
	chatCollection    *mongo.Collection
	messageCollection *mongo.Collection
	redisRepo         redis.IRedisRepositories
	cacheLocks        map[string]*sync.RWMutex // Per-chat cache locks
	locksMutex        sync.Mutex               // Protects cacheLocks map
}

func NewChatRepository(mongoClient *mongodb.MongoDBClient, redisRepo redis.IRedisRepositories) ChatRepository {
	return &chatRepository{
		chatCollection:    mongoClient.GetCollectionByName("chats"),
		messageCollection: mongoClient.GetCollectionByName("messages"),
		redisRepo:         redisRepo,
		cacheLocks:        make(map[string]*sync.RWMutex),
	}
}

// getCacheLock returns or creates a mutex for a specific cache key
func (r *chatRepository) getCacheLock(cacheKey string) *sync.RWMutex {
	r.locksMutex.Lock()
	defer r.locksMutex.Unlock()

	if lock, exists := r.cacheLocks[cacheKey]; exists {
		return lock
	}

	// Create new lock for this cache key
	lock := &sync.RWMutex{}
	r.cacheLocks[cacheKey] = lock
	log.Printf("[CACHE LOCK] Created new mutex for key: %s", cacheKey)
	return lock
}

// Chat documents are read from primary. Never warm an unfenced snapshot into
// Redis: an older in-flight reader could otherwise resurrect a previous binding.
func (r *chatRepository) updateChatCache(chatID primitive.ObjectID) {
	if r.redisRepo != nil {
		r.redisRepo.Del(fmt.Sprintf("chat:id:%s", chatID.Hex()), context.Background())
	}
}

// addMessageToCache adds a new message to the sliding window cache (last 50 messages)
func (r *chatRepository) addMessageToCache(message *models.Message) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("chat:%s:messages:recent", message.ChatID.Hex())

	msgData, err := json.Marshal(message)
	if err != nil {
		log.Printf("[CACHE ERROR] Failed to marshal message for caching: %v", err)
		return
	}

	// Add message to the BEGINNING of the list (LPush) - newest first
	if err := r.redisRepo.LPush(cacheKey, [][]byte{msgData}, constants.MessageCacheTTL, ctx); err != nil {
		log.Printf("[CACHE ERROR] Failed to add message to cache: %v", err)
		return
	}

	// Trim to keep only first 50 messages (newest 50)
	if err := r.redisRepo.LTrim(cacheKey, 0, constants.MaxCachedMessages-1, ctx); err != nil {
		log.Printf("[CACHE ERROR] Failed to trim message cache: %v", err)
	} else {
		log.Printf("[CACHE WRITE SUCCESS] Added message to sliding window - ChatID: %s", message.ChatID.Hex())
	}
}

// getMessagesFromCache retrieves messages from cache with smart range selection
// Returns (messages, cacheHitCount, error)
func (r *chatRepository) getMessagesFromCache(chatID primitive.ObjectID, skip, limit int) ([]*models.Message, int, error) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("chat:%s:messages:recent", chatID.Hex())

	// Check cache size
	cacheSize, err := r.redisRepo.LLen(cacheKey, ctx)
	if err != nil || cacheSize == 0 {
		log.Printf("[CACHE MISS] No messages in cache - ChatID: %s", chatID.Hex())
		return nil, 0, nil
	}

	log.Printf("[CACHE] Cache has %d messages for ChatID: %s, requesting skip:%d, limit:%d", cacheSize, chatID.Hex(), skip, limit)

	// Calculate which messages we can get from cache
	// Cache stores newest first (index 0 = newest), we need reverse order for our API
	// If user wants skip=0, limit=20: Get last 20 from cache (indices -20 to -1)
	// If user wants skip=10, limit=20: Get messages at indices -30 to -11

	requestedEnd := skip + limit
	if int64(requestedEnd) > cacheSize {
		// Not all requested messages are in cache
		canFetchFromCache := int(cacheSize) - skip
		if canFetchFromCache <= 0 {
			log.Printf("[CACHE PARTIAL] Requested range beyond cache - skip:%d exceeds cache size:%d", skip, cacheSize)
			return nil, 0, nil
		}

		// Fetch what we can from cache
		start := -int64(cacheSize) + int64(skip)
		stop := -1 - int64(skip)

		if stop < start {
			return nil, 0, nil
		}

		msgBytes, err := r.redisRepo.LRange(cacheKey, start, stop, ctx)
		if err != nil {
			log.Printf("[CACHE ERROR] Failed to fetch messages from cache: %v", err)
			return nil, 0, err
		}

		messages := make([]*models.Message, len(msgBytes))
		for i, data := range msgBytes {
			var msg models.Message
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("[CACHE ERROR] Failed to unmarshal cached message: %v", err)
				continue
			}
			messages[i] = &msg
		}

		log.Printf("[CACHE PARTIAL HIT] Fetched %d messages from cache (need %d more from DB)", len(messages), limit-len(messages))
		return messages, len(messages), nil
	}

	// All requested messages are in cache
	start := -int64(requestedEnd)
	stop := -1 - int64(skip)

	msgBytes, err := r.redisRepo.LRange(cacheKey, start, stop, ctx)
	if err != nil {
		log.Printf("[CACHE ERROR] Failed to fetch messages from cache: %v", err)
		return nil, 0, err
	}

	messages := make([]*models.Message, len(msgBytes))
	for i, data := range msgBytes {
		var msg models.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[CACHE ERROR] Failed to unmarshal cached message: %v", err)
			continue
		}
		messages[i] = &msg
	}

	log.Printf("[CACHE HIT] Fetched %d messages entirely from cache - ChatID: %s", len(messages), chatID.Hex())

	// Refresh TTL on access (sliding expiration for active conversations)
	go func() {
		if err := r.redisRepo.Expire(cacheKey, constants.MessageCacheTTL, ctx); err != nil {
			log.Printf("[CACHE] Failed to refresh TTL: %v", err)
		} else {
			log.Printf("[CACHE] Refreshed TTL for active conversation - ChatID: %s", chatID.Hex())
		}
	}()

	return messages, len(messages), nil
}

// invalidateMessageCache removes message cache for a chat
func (r *chatRepository) invalidateMessageCache(chatID primitive.ObjectID) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("chat:%s:messages:recent", chatID.Hex())

	if err := r.redisRepo.Del(cacheKey, ctx); err != nil {
		log.Printf("[CACHE ERROR] Failed to invalidate message cache: %v", err)
	} else {
		log.Printf("[CACHE INVALIDATE SUCCESS] Invalidated message cache - ChatID: %s", chatID.Hex())
	}
}

// updateMessageInCache updates a single message in the cached list
func (r *chatRepository) updateMessageInCache(message *models.Message) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("chat:%s:messages:recent", message.ChatID.Hex())

	// Acquire write lock for this chat's cache
	lock := r.getCacheLock(cacheKey)
	lock.Lock()
	defer lock.Unlock()

	log.Printf("[CACHE LOCK] Acquired write lock for update - ChatID: %s", message.ChatID.Hex())

	// Fetch entire list
	msgBytes, err := r.redisRepo.LRange(cacheKey, 0, -1, ctx)
	if err != nil || len(msgBytes) == 0 {
		log.Printf("[CACHE SKIP] No cache to update - ChatID: %s", message.ChatID.Hex())
		return
	}

	// Find and update the message in list
	updated := false
	updatedList := make([][]byte, 0, len(msgBytes))
	for _, data := range msgBytes {
		var msg models.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[CACHE ERROR] Failed to unmarshal cached message: %v", err)
			continue
		}

		// If this is the message to update, replace it
		if msg.ID == message.ID {
			newData, _ := json.Marshal(message)
			updatedList = append(updatedList, newData)
			updated = true
			log.Printf("[CACHE UPDATE] Found and updated message in cache - MessageID: %s", message.ID.Hex())
		} else {
			updatedList = append(updatedList, data)
		}
	}

	if !updated {
		log.Printf("[CACHE SKIP] Message not found in cache - MessageID: %s", message.ID.Hex())
		return
	}

	// Delete old list and write updated list
	r.redisRepo.Del(cacheKey, ctx)
	for _, data := range updatedList {
		r.redisRepo.RPush(cacheKey, [][]byte{data}, constants.MessageCacheTTL, ctx)
	}

	log.Printf("[CACHE UPDATE SUCCESS] Updated message in cache list - ChatID: %s", message.ChatID.Hex())
}

// deleteMessageFromCache removes a single message from the cached list
func (r *chatRepository) deleteMessageFromCache(chatID, messageID primitive.ObjectID) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("chat:%s:messages:recent", chatID.Hex())

	// Acquire write lock for this chat's cache
	lock := r.getCacheLock(cacheKey)
	lock.Lock()
	defer lock.Unlock()

	log.Printf("[CACHE LOCK] Acquired write lock for delete - ChatID: %s", chatID.Hex())

	// Fetch entire list
	msgBytes, err := r.redisRepo.LRange(cacheKey, 0, -1, ctx)
	if err != nil || len(msgBytes) == 0 {
		log.Printf("[CACHE SKIP] No cache to update - ChatID: %s", chatID.Hex())
		return
	}

	// Filter out the deleted message
	found := false
	updatedList := make([][]byte, 0, len(msgBytes))
	for _, data := range msgBytes {
		var msg models.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		// Skip the message to delete
		if msg.ID == messageID {
			found = true
			log.Printf("[CACHE DELETE] Found and removing message from cache - MessageID: %s", messageID.Hex())
			continue
		}
		updatedList = append(updatedList, data)
	}

	if !found {
		log.Printf("[CACHE SKIP] Message not found in cache - MessageID: %s", messageID.Hex())
		return
	}

	// Delete old list and write updated list
	r.redisRepo.Del(cacheKey, ctx)
	if len(updatedList) > 0 {
		for _, data := range updatedList {
			r.redisRepo.RPush(cacheKey, [][]byte{data}, constants.MessageCacheTTL, ctx)
		}
		log.Printf("[CACHE DELETE SUCCESS] Removed message from cache list - ChatID: %s", chatID.Hex())
	} else {
		log.Printf("[CACHE DELETE] Cache is now empty - ChatID: %s", chatID.Hex())
	}
}

// cachePinnedMessages caches pinned messages for a chat (only if at least 1 exists)
func (r *chatRepository) cachePinnedMessages(chatID primitive.ObjectID, messages []models.Message) {
	// Only cache if at least 1 pinned message exists
	if len(messages) == 0 {
		log.Printf("[CACHE SKIP] No pinned messages to cache - ChatID: %s", chatID.Hex())
		return
	}

	ctx := context.Background()
	cacheKey := fmt.Sprintf("chat:%s:pinned", chatID.Hex())

	data, err := json.Marshal(messages)
	if err != nil {
		log.Printf("[CACHE ERROR] Failed to marshal pinned messages: %v", err)
		return
	}

	if err := r.redisRepo.SetCompressed(cacheKey, data, constants.ChatCacheTTL, ctx); err != nil {
		log.Printf("[CACHE ERROR] Failed to cache pinned messages: %v", err)
	} else {
		log.Printf("[CACHE WRITE SUCCESS] Cached %d pinned messages - ChatID: %s", len(messages), chatID.Hex())
	}
}

// refreshPinnedCache fetches pinned messages from DB and updates cache
func (r *chatRepository) refreshPinnedCache(chatID primitive.ObjectID) {
	ctx := context.Background()
	log.Printf("[CACHE REFRESH] Refreshing pinned messages cache - ChatID: %s", chatID.Hex())

	var messages []models.Message
	filter := bson.M{
		"chat_id":   chatID,
		"is_pinned": true,
	}

	opts := options.Find().SetSort(bson.D{{Key: "pinned_at", Value: -1}})

	cursor, err := r.messageCollection.Find(ctx, filter, opts)
	if err != nil {
		log.Printf("[CACHE ERROR] Failed to fetch pinned messages for refresh: %v", err)
		return
	}
	defer cursor.Close(ctx)

	if err := cursor.All(ctx, &messages); err != nil {
		log.Printf("[CACHE ERROR] Failed to decode pinned messages: %v", err)
		return
	}

	// If no pinned messages exist, invalidate the cache
	if len(messages) == 0 {
		cacheKey := fmt.Sprintf("chat:%s:pinned", chatID.Hex())
		r.redisRepo.Del(cacheKey, ctx)
		log.Printf("[CACHE INVALIDATE] No pinned messages found, removed cache - ChatID: %s", chatID.Hex())
		return
	}

	// Cache the refreshed list
	r.cachePinnedMessages(chatID, messages)
}

func (r *chatRepository) Create(chat *models.Chat) error {
	_, err := r.primaryChats().InsertOne(context.Background(), chat)
	if err == nil {
		r.updateChatCache(chat.ID)
	}
	return err
}

var ErrChatBindingConflict = errors.New("ERP_SOURCE_BINDING_CONFLICT")

type ChatMetadataPatch struct {
	DisplayName               *string
	SelectedCollections       *string
	PreferredLLMModel         *string
	PreferredReasoningLevel   *string
	AutoExecuteQuery          *bool
	NonTechMode               *bool
	AutoGenerateVisualization *bool
	AutoAnalyzeResults        *bool
}

func (r *chatRepository) primaryChats() *mongo.Collection {
	collection, _ := r.chatCollection.Clone(options.Collection().SetReadPreference(readpref.Primary()).SetReadConcern(readconcern.Majority()).SetWriteConcern(writeconcern.Majority()))
	return collection
}

func (r *chatRepository) ReadCurrent(ctx context.Context, owner, id primitive.ObjectID) (*models.Chat, error) {
	var chat models.Chat
	if err := r.primaryChats().FindOne(ctx, bson.M{"_id": id, "user_id": owner}).Decode(&chat); err != nil {
		return nil, err
	}
	return &chat, nil
}

func chatBindingFilter(chat *models.Chat) bson.M {
	filter := bson.M{"_id": chat.ID, "user_id": chat.UserID}
	fields := bson.M{"source_id": chat.SourceID, "source_binding_revision": chat.SourceBindingRevision,
		"source_connection_fingerprint": chat.SourceConnectionFingerprint, "source_binding_confirmed": chat.SourceBindingConfirmed,
		"erp_associated": chat.ERPAssociated, "connection_revision": chat.ConnectionRevision, "selection_revision": chat.SelectionRevision}
	for field, value := range fields {
		if value == "" || value == int64(0) || value == false {
			filter[field] = bson.M{"$in": bson.A{nil, value}}
		} else {
			filter[field] = value
		}
	}
	return filter
}

func (r *chatRepository) patchChat(ctx context.Context, filter, fields bson.M) error {
	fields["updated_at"] = time.Now()
	result, err := r.primaryChats().UpdateOne(ctx, filter, bson.M{"$set": fields})
	// Even a lost acknowledgement may have committed. No Chat cache is ever read
	// or filled; invalidate legacy entries synchronously on either outcome.
	r.updateChatCache(filter["_id"].(primitive.ObjectID))
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return ErrChatBindingConflict
	}
	return nil
}

func requireBindingWrite(ctx context.Context, previous, next *models.Chat) error {
	for _, candidate := range []*models.Chat{previous, next} {
		if candidate.SourceID == "" {
			continue
		}
		identity, err := sourcegate.ContextIdentity(ctx, candidate.SourceID)
		if err != nil {
			return err
		}
		lease, err := sourcegate.FromContext(ctx, identity)
		if err != nil {
			return err
		}
		if lease.Mode() != sourcegate.Exclusive || identity.OwnerID != next.UserID.Hex() || identity.ChatID != next.ID.Hex() {
			return sourcegate.ErrIdentity
		}
	}
	return nil
}

func (r *chatRepository) MarkPendingCAS(ctx context.Context, previous, pending *models.Chat) error {
	if previous.ID != pending.ID || previous.UserID != pending.UserID || !pending.ERPAssociated || pending.SourceBindingConfirmed ||
		pending.SourceBindingRevision != previous.SourceBindingRevision+1 || pending.ConnectionRevision != previous.ConnectionRevision+1 {
		return ErrChatBindingConflict
	}
	if err := requireBindingWrite(ctx, previous, pending); err != nil {
		return err
	}
	return r.patchChat(ctx, chatBindingFilter(previous), bson.M{"source_id": pending.SourceID, "source_binding_revision": pending.SourceBindingRevision,
		"source_connection_fingerprint": pending.SourceConnectionFingerprint, "source_binding_confirmed": false,
		"erp_associated": true, "connection_revision": pending.ConnectionRevision, "object_state": nil})
}

func (r *chatRepository) CommitBindingCAS(ctx context.Context, pending, confirmed *models.Chat) error {
	if pending.ID != confirmed.ID || pending.UserID != confirmed.UserID || pending.SourceID != confirmed.SourceID ||
		pending.SourceBindingRevision != confirmed.SourceBindingRevision || pending.ConnectionRevision != confirmed.ConnectionRevision ||
		pending.SourceConnectionFingerprint != confirmed.SourceConnectionFingerprint || pending.SourceBindingConfirmed || !confirmed.SourceBindingConfirmed || !confirmed.ERPAssociated {
		return ErrChatBindingConflict
	}
	if err := requireBindingWrite(ctx, pending, confirmed); err != nil {
		return err
	}
	connection := confirmed.Connection
	connection.CurrentSchema, connection.SchemaUpdatedAt = nil, nil
	fields, err := objectRebindingFields(pending, confirmed)
	if err != nil {
		return err
	}
	fields["connection"], fields["source_binding_confirmed"], fields["object_state"] = connection, true, nil
	return r.patchChat(ctx, chatBindingFilter(pending), fields)
}

func (r *chatRepository) PatchMetadata(ctx context.Context, owner, id primitive.ObjectID, patch ChatMetadataPatch) error {
	fields := bson.M{}
	if patch.DisplayName != nil {
		fields["display_name"] = *patch.DisplayName
	}
	if patch.SelectedCollections != nil {
		fields["selected_collections"] = *patch.SelectedCollections
	}
	if patch.PreferredLLMModel != nil {
		fields["preferred_llm_model"] = *patch.PreferredLLMModel
	}
	if patch.PreferredReasoningLevel != nil {
		fields["preferred_reasoning_level"] = *patch.PreferredReasoningLevel
	}
	if patch.AutoExecuteQuery != nil {
		fields["settings.auto_execute_query"] = *patch.AutoExecuteQuery
	}
	if patch.NonTechMode != nil {
		fields["settings.non_tech_mode"] = *patch.NonTechMode
	}
	if patch.AutoGenerateVisualization != nil {
		fields["settings.auto_generate_visualization"] = *patch.AutoGenerateVisualization
	}
	if patch.AutoAnalyzeResults != nil {
		fields["settings.auto_analyze_results"] = *patch.AutoAnalyzeResults
	}
	if len(fields) == 0 {
		return nil
	}
	filter := bson.M{"_id": id, "user_id": owner}
	if patch.SelectedCollections != nil {
		current, err := r.ReadCurrent(ctx, owner, id)
		if err != nil {
			return err
		}
		if current.SelectedObjects != nil {
			return ErrObjectConflict
		}
		filter = chatBindingFilter(current)
		filter["selection_revision"] = revisionMatch(current.SelectionRevision)
		filter["selected_objects"] = nil
		// A same-selection metadata PATCH must still pass the binding/selection
		// CAS above, but does not invalidate an unchanged schema or revision.
		if *patch.SelectedCollections == current.SelectedCollections {
			delete(fields, "selected_collections")
		} else {
			fields["selection_revision"] = current.SelectionRevision + 1
			fields["connection.current_schema"], fields["connection.schema_updated_at"] = nil, nil
		}
	}
	return r.patchChat(ctx, filter, fields)
}

func (r *chatRepository) UpdateIndependentConnectionCAS(ctx context.Context, previous *models.Chat, connection models.Connection) error {
	if previous.ERPAssociated || previous.SourceID != "" || previous.SourceBindingRevision != 0 || previous.SelectedObjects != nil {
		return ErrChatBindingConflict
	}
	connection.CurrentSchema, connection.SchemaUpdatedAt = nil, nil
	return r.patchChat(ctx, chatBindingFilter(previous), bson.M{"connection": connection, "connection_revision": previous.ConnectionRevision + 1, "object_state": nil})
}

func (r *chatRepository) UpdateSpreadsheetNameCAS(ctx context.Context, previous *models.Chat, name string) error {
	if previous.ERPAssociated || previous.SourceID != "" || previous.SourceBindingRevision != 0 || previous.Connection.Type != constants.DatabaseTypeSpreadsheet {
		return ErrChatBindingConflict
	}
	filter := chatBindingFilter(previous)
	filter["connection.database"] = previous.Connection.Database
	return r.patchChat(ctx, filter, bson.M{"connection.database": name})
}

func (r *chatRepository) UpdateConnectionSchema(ctx context.Context, origin *models.Chat, schema string) error {
	filter := chatBindingFilter(origin)
	filter["selection_revision"] = revisionMatch(origin.SelectionRevision)
	return r.patchChat(ctx, filter, bson.M{"connection.current_schema": schema,
		"connection.schema_updated_at": primitive.NewDateTimeFromTime(time.Now())})
}

func (r *chatRepository) Delete(id primitive.ObjectID) error {
	filter := bson.M{"_id": id}
	_, err := r.chatCollection.DeleteOne(context.Background(), filter)

	if err == nil {
		// Invalidate all cache for this chat
		go func() {
			ctx := context.Background()
			r.redisRepo.Del(fmt.Sprintf("chat:id:%s", id.Hex()), ctx)
			r.invalidateMessageCache(id)
			r.redisRepo.Del(fmt.Sprintf("chat:%s:pinned", id.Hex()), ctx)
		}()
	}

	return err
}

func (r *chatRepository) FindByID(id primitive.ObjectID) (*models.Chat, error) {
	var chat models.Chat
	err := r.primaryChats().FindOne(context.Background(), bson.M{"_id": id}).Decode(&chat)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &chat, nil
}

func (r *chatRepository) FindByUserID(userID primitive.ObjectID, page, pageSize int) ([]*models.Chat, int64, error) {
	var chats []*models.Chat
	filter := bson.M{"user_id": userID}

	// Get total count
	total, err := r.chatCollection.CountDocuments(context.Background(), filter)
	if err != nil {
		return nil, 0, err
	}

	// Setup pagination
	skip := int64((page - 1) * pageSize)
	opts := options.Find().
		SetSkip(skip).
		SetLimit(int64(pageSize)).
		SetSort(bson.D{{Key: "created_at", Value: -1}})

	cursor, err := r.chatCollection.Find(context.Background(), filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(context.Background())

	err = cursor.All(context.Background(), &chats)
	return chats, total, err
}

func (r *chatRepository) CreateMessage(message *models.Message) error {
	log.Printf("CreateMessage -> message: %v", message)
	r.updateChatTimeStamp(message.ChatID)
	_, err := r.messageCollection.InsertOne(context.Background(), message)

	if err == nil {
		// Add message to sliding window cache
		go r.addMessageToCache(message)

		// If message is created as pinned, refresh pinned cache
		if message.IsPinned {
			log.Printf("[CACHE] Message created as pinned, refreshing pinned cache - MessageID: %s", message.ID.Hex())
			go r.refreshPinnedCache(message.ChatID)
		}
	}

	return err
}

func (r *chatRepository) UpdateMessage(id primitive.ObjectID, message *models.Message) error {
	r.updateChatTimeStamp(message.ChatID)
	message.UpdatedAt = time.Now()
	filter := bson.M{"_id": id}
	update := bson.M{"$set": message}
	_, err := r.messageCollection.UpdateOne(context.Background(), filter, update)

	if err == nil {
		// Update message in cached list (in-place update)
		go r.updateMessageInCache(message)

		// If message is pinned or was pinned, refresh pinned cache
		if message.IsPinned {
			log.Printf("[CACHE] Message is pinned, refreshing pinned cache - MessageID: %s", message.ID.Hex())
			go r.refreshPinnedCache(message.ChatID)
		}
	}

	return err
}

func (r *chatRepository) DeleteMessages(chatID primitive.ObjectID) error {
	filter := bson.M{"chat_id": chatID}
	_, err := r.messageCollection.DeleteMany(context.Background(), filter)

	if err == nil {
		go func() {
			r.invalidateMessageCache(chatID)
			// Also invalidate pinned messages cache
			ctx := context.Background()
			cacheKey := fmt.Sprintf("chat:%s:pinned", chatID.Hex())
			r.redisRepo.Del(cacheKey, ctx)
			log.Printf("[CACHE INVALIDATE] Invalidated pinned cache after bulk delete - ChatID: %s", chatID.Hex())
		}()
	}

	return err
}

func (r *chatRepository) FindMessagesByChat(chatID primitive.ObjectID, page, pageSize int) ([]*models.Message, int64, error) {
	var messages []*models.Message
	filter := bson.M{"chat_id": chatID}

	// Get total count from DB
	total, err := r.messageCollection.CountDocuments(context.Background(), filter)
	if err != nil {
		return nil, 0, err
	}

	// Calculate skip
	skip := (page - 1) * pageSize

	// Try smart cache+DB hybrid fetch
	log.Printf("[PAGINATION] FindMessagesByChat - ChatID: %s, page: %d, pageSize: %d, skip: %d", chatID.Hex(), page, pageSize, skip)

	cachedMessages, cacheHitCount, err := r.getMessagesFromCache(chatID, skip, pageSize)
	if err == nil && cacheHitCount > 0 {
		// Got some or all messages from cache
		if cacheHitCount == pageSize {
			// All messages from cache - perfect!
			log.Printf("[PAGINATION] Full cache hit - %d messages from cache", cacheHitCount)
			return cachedMessages, total, nil
		}

		// Partial hit - need more from DB
		neededFromDB := pageSize - cacheHitCount
		dbSkip := skip + cacheHitCount

		log.Printf("[PAGINATION] Partial cache hit - %d from cache, fetching %d from DB (skip: %d)", cacheHitCount, neededFromDB, dbSkip)

		opts := options.Find().
			SetSort(bson.D{{Key: "created_at", Value: -1}}).
			SetSkip(int64(dbSkip)).
			SetLimit(int64(neededFromDB))

		cursor, err := r.messageCollection.Find(context.Background(), filter, opts)
		if err != nil {
			return nil, 0, err
		}
		defer cursor.Close(context.Background())

		var dbMessages []*models.Message
		if err := cursor.All(context.Background(), &dbMessages); err != nil {
			return nil, 0, err
		}

		// Combine: cache messages + DB messages
		messages = append(cachedMessages, dbMessages...)
		log.Printf("[PAGINATION] Hybrid fetch complete - %d from cache + %d from DB = %d total", cacheHitCount, len(dbMessages), len(messages))
		return messages, total, nil
	}

	// Cache miss or error - fetch all from DB
	log.Printf("[PAGINATION] Cache miss - fetching all from DB")
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip(int64(skip)).
		SetLimit(int64(pageSize))

	cursor, err := r.messageCollection.Find(context.Background(), filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(context.Background())

	err = cursor.All(context.Background(), &messages)

	// Populate cache for future requests (only if fetching first page)
	if err == nil && page == 1 && len(messages) > 0 {
		go func() {
			ctx := context.Background()
			cacheKey := fmt.Sprintf("chat:%s:messages:recent", chatID.Hex())

			// Cache up to 50 most recent messages
			for _, msg := range messages {
				msgData, _ := json.Marshal(msg)
				r.redisRepo.RPush(cacheKey, [][]byte{msgData}, constants.MessageCacheTTL, ctx)
			}
			r.redisRepo.LTrim(cacheKey, -constants.MaxCachedMessages, -1, ctx)
			log.Printf("[CACHE POPULATE] Cached %d messages for ChatID: %s", len(messages), chatID.Hex())
		}()
	}

	return messages, total, err
}

func (r *chatRepository) FindLatestMessageByChat(chatID primitive.ObjectID, page, pageSize int) ([]*models.Message, int64, error) {
	var messages []*models.Message
	filter := bson.M{"chat_id": chatID}

	// Get total count
	total, err := r.messageCollection.CountDocuments(context.Background(), filter)
	if err != nil {
		return nil, 0, err
	}

	// Setup pagination
	skip := int64((page - 1) * pageSize)
	opts := options.Find().
		SetSkip(skip).
		SetLimit(int64(pageSize)).
		SetSort(bson.D{{Key: "created_at", Value: -1}}) // Descending order for messages, ex: latest message will be first

	cursor, err := r.messageCollection.Find(context.Background(), filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(context.Background())

	err = cursor.All(context.Background(), &messages)
	return messages, total, err
}

func (r *chatRepository) FindMessageByID(id primitive.ObjectID) (*models.Message, error) {
	var message models.Message
	err := r.messageCollection.FindOne(context.Background(), bson.M{"_id": id}).Decode(&message)
	return &message, err
}

// UpdateChatTimestamp updates the chat's updated_at timestamp asynchronously
// This is called on dashboard operations (create/update/delete/refresh) to show "last active" time
func (r *chatRepository) UpdateChatTimestamp(chatID primitive.ObjectID) error {
	return r.updateChatTimeStamp(chatID)
}

func (r *chatRepository) updateChatTimeStamp(chatID primitive.ObjectID) error {
	go func() {
		filter := bson.M{"_id": chatID}
		update := bson.M{"$set": bson.M{"updated_at": time.Now()}}
		_, err := r.chatCollection.UpdateOne(context.Background(), filter, update)
		if err != nil {
			log.Printf("Error updating chat timestamp: %v", err)
		}
		// Invalidate chat cache after timestamp update
		go r.updateChatCache(chatID)
	}()
	return nil
}

// FindNextMessageByID finds the next message by ID of the previous user message (ex: if the previous message is a user message, find the next message that has userMessageId as the previous message id and is an assistant message)
func (r *chatRepository) FindNextMessageByID(id primitive.ObjectID) (*models.Message, error) {
	// First, find the current message to get its chat ID
	currentMsg, err := r.FindMessageByID(id)
	if err != nil {
		return nil, err
	}
	if currentMsg == nil {
		return nil, mongo.ErrNoDocuments
	}

	// If this is a user message, find the AI message that has this message ID as its UserMessageId
	if currentMsg.Type == "user" {
		filter := bson.M{
			"chat_id":         currentMsg.ChatID,
			"user_message_id": id,
			"type":            "assistant",
		}

		var aiMsg models.Message
		err = r.messageCollection.FindOne(context.Background(), filter).Decode(&aiMsg)
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return &aiMsg, err
	} else {
		// If it's not a user message, fall back to timestamp-based search
		filter := bson.M{
			"chat_id": currentMsg.ChatID,
			"created_at": bson.M{
				"$gt": currentMsg.CreatedAt,
			},
		}

		opts := options.FindOne().SetSort(bson.D{{Key: "created_at", Value: 1}})

		var nextMsg models.Message
		err = r.messageCollection.FindOne(context.Background(), filter, opts).Decode(&nextMsg)
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return &nextMsg, err
	}
}

// FindPinnedMessagesByChat finds all pinned messages for a chat, sorted by pinnedAt descending
func (r *chatRepository) FindPinnedMessagesByChat(chatID primitive.ObjectID) ([]models.Message, error) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("chat:%s:pinned", chatID.Hex())

	// Try cache first
	log.Printf("[CACHE] Attempting to fetch pinned messages from cache - ChatID: %s", chatID.Hex())
	if compressed, err := r.redisRepo.GetCompressed(cacheKey, ctx); err == nil {
		var messages []models.Message
		if err := json.Unmarshal(compressed, &messages); err == nil {
			log.Printf("[CACHE HIT] Retrieved %d pinned messages from cache", len(messages))
			return messages, nil
		}
	}

	// Cache miss - query database
	log.Printf("[CACHE MISS] Querying database for pinned messages")
	var messages []models.Message
	filter := bson.M{
		"chat_id":   chatID,
		"is_pinned": true,
	}

	// Sort by pinnedAt descending (latest first)
	opts := options.Find().SetSort(bson.D{{Key: "pinned_at", Value: -1}})

	cursor, err := r.messageCollection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	err = cursor.All(ctx, &messages)

	// Cache the result
	if err == nil {
		go r.cachePinnedMessages(chatID, messages)
	}

	return messages, err
}

// FindMessagesByChatAfterTime finds messages in a chat created after a specific time
func (r *chatRepository) FindMessagesByChatAfterTime(chatID primitive.ObjectID, after time.Time, page, pageSize int) ([]models.Message, int64, error) {
	var messages []models.Message
	filter := bson.M{
		"chat_id": chatID,
		"created_at": bson.M{
			"$gt": after,
		},
	}

	// Get total count
	total, err := r.messageCollection.CountDocuments(context.Background(), filter)
	if err != nil {
		return nil, 0, err
	}

	// Setup pagination
	skip := int64((page - 1) * pageSize)
	opts := options.Find().
		SetSkip(skip).
		SetLimit(int64(pageSize)).
		SetSort(bson.D{{Key: "created_at", Value: -1}}) // Descending order

	cursor, err := r.messageCollection.Find(context.Background(), filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(context.Background())

	err = cursor.All(context.Background(), &messages)
	return messages, total, err
}

// UpdateQueryVisualizationID updates a query's visualization ID within a message
func (r *chatRepository) UpdateQueryVisualizationID(messageID, queryID, visualizationID primitive.ObjectID) error {
	log.Printf("UpdateQueryVisualizationID -> Attempting to update: messageID=%s, queryID=%s, visualizationID=%s",
		messageID.Hex(), queryID.Hex(), visualizationID.Hex())

	filter := bson.M{
		"_id":        messageID,
		"queries.id": queryID,
	}
	update := bson.M{
		"$set": bson.M{
			"queries.$.visualization_id": visualizationID,
		},
	}

	result, err := r.messageCollection.UpdateOne(context.Background(), filter, update)
	if err != nil {
		log.Printf("UpdateQueryVisualizationID -> Error: %v", err)
		return err
	}

	log.Printf("UpdateQueryVisualizationID -> Matched: %d, Modified: %d, VisualizationID: %s",
		result.MatchedCount, result.ModifiedCount, visualizationID.Hex())

	if result.MatchedCount == 0 {
		log.Printf("UpdateQueryVisualizationID -> WARNING: No documents matched! Filter: %+v", filter)
	}
	if result.ModifiedCount == 0 {
		log.Printf("UpdateQueryVisualizationID -> WARNING: No documents modified! Filter: %+v", filter)
	}

	return nil
}
