package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
)

var (
	ErrObjectKnowledgeScope    = errors.New("knowledge scope mismatch")
	ErrObjectKnowledgeSnapshot = errors.New("invalid knowledge snapshot")
	ErrObjectKnowledgeRevision = errors.New("knowledge revision conflict")
	ErrObjectKnowledgeMapping  = errors.New("knowledge mapping unavailable")
	ErrObjectKnowledgeData     = errors.New("invalid knowledge revision data")
)

// ObjectKnowledgeProjection separates current enrichment from retained source
// text. History may contain inaccessible names/text: never send it to retrieval,
// an LLM or a structure tool. Only Effective is eligible for those consumers.
type ObjectKnowledgeProjection struct {
	KBRevision string
	Effective  []models.TableDescription
	History    []models.TableDescription
}

// ProjectObjectKnowledge is a pure projection of a server-authorized, selected,
// currently readable T01 snapshot and the stored KB. It does not authorize reads,
// acquire source leases, persist changes or call any model. Missing descriptions
// never make structural objects unavailable. KBRevision is derived, not trusted
// from kb.KBRevision, and covers history order as well as effective content.
func ProjectObjectKnowledge(snapshot schemaobjects.Snapshot, kb *models.KnowledgeBase) (ObjectKnowledgeProjection, error) {
	objects, err := knowledgeObjects(snapshot)
	if err != nil {
		return ObjectKnowledgeProjection{}, err
	}
	if kb != nil && (kb.UserID.IsZero() || kb.ChatID.IsZero() ||
		kb.UserID.Hex() != snapshot.Scope.OwnerID || kb.ChatID.Hex() != snapshot.Scope.ChatID) {
		return ObjectKnowledgeProjection{}, ErrObjectKnowledgeScope
	}
	result := ObjectKnowledgeProjection{
		Effective: make([]models.TableDescription, 0, len(objects)),
		History:   []models.TableDescription{},
	}
	if kb != nil {
		result.History = cloneKnowledgeDescriptions(kb.TableDescriptions)
	}
	byID := make(map[string]schemaobjects.Object, len(objects))
	for _, object := range objects {
		byID[object.Metadata.ObjectID] = object
	}
	for i := range result.History {
		row := &result.History[i]
		object, exists := byID[row.ObjectID]
		matches := exists && knowledgeAssociationMatches(*row, object, snapshot.Scope)
		row.Provenance = knowledgeProvenance(row.Provenance)
		row.State = models.KnowledgePending
		if matches {
			row.State = models.KnowledgeMapped
		}
		columns := knowledgeColumnTypes(object)
		for j := range row.FieldDescriptions {
			field := &row.FieldDescriptions[j]
			mapped := matches && field.State == models.KnowledgeMapped &&
				field.ColumnType != "" && columns[field.FieldName] == field.ColumnType
			field.Provenance = knowledgeProvenance(field.Provenance)
			field.State = models.KnowledgePending
			if mapped {
				field.State = models.KnowledgeMapped
			}
		}
	}
	for _, object := range objects {
		scope := snapshot.Scope
		row := models.TableDescription{
			TableName: schemaobjects.SQLIdentifier(object.Metadata.Ref),
			ObjectID:  object.Metadata.ObjectID, InstanceVersion: object.Metadata.InstanceVersion,
			ObjectScope: &scope, State: models.KnowledgePending,
			FieldDescriptions: []models.FieldDescription{},
		}
		if object.Metadata.InstanceVersion != "" {
			row.State = models.KnowledgeMapped
			if strings.TrimSpace(row.Description) == "" && strings.TrimSpace(object.Comment) != "" {
				row.Description, row.Provenance = object.Comment, models.KnowledgeDatabaseComment
			}
			for _, history := range result.History {
				if history.State != models.KnowledgeMapped || history.ObjectID != row.ObjectID {
					continue
				}
				if history.Provenance != models.KnowledgeDatabaseComment &&
					preferKnowledgeText(row.Description, row.Provenance, history.Description, history.Provenance) {
					row.Description, row.Provenance, row.UpdatedAt = history.Description, history.Provenance, history.UpdatedAt
				}
			}

			for _, column := range object.Columns {
				field := models.FieldDescription{FieldName: column.Name, ColumnType: column.Type, State: models.KnowledgeMapped}
				if strings.TrimSpace(field.Description) == "" && strings.TrimSpace(column.Comment) != "" {
					field.Description, field.Provenance = column.Comment, models.KnowledgeDatabaseComment
				}
				for _, history := range result.History {
					if history.State != models.KnowledgeMapped || history.ObjectID != row.ObjectID {
						continue
					}
					for _, candidate := range history.FieldDescriptions {
						if candidate.State == models.KnowledgeMapped && candidate.FieldName == column.Name &&
							candidate.Provenance != models.KnowledgeDatabaseComment &&
							preferKnowledgeText(field.Description, field.Provenance, candidate.Description, candidate.Provenance) {
							field = candidate
						}
					}
				}

				if strings.TrimSpace(field.Description) != "" {
					row.FieldDescriptions = append(row.FieldDescriptions, field)
				}
			}
		}
		result.Effective = append(result.Effective, row)
	}
	// Objects have canonical order and no volatile counts. History order is
	// intentional: confirmation addresses a row by index and must detect reorder.
	result.KBRevision, err = knowledgeDigest(struct {
		Version            int
		Scope              schemaobjects.Scope
		Objects            []schemaobjects.Object
		Effective, History []models.TableDescription
	}{1, snapshot.Scope, objects, knowledgeRevisionRows(result.Effective), knowledgeRevisionRows(result.History)})
	if err != nil {
		return ObjectKnowledgeProjection{}, err
	}
	return result, nil
}

// ConfirmObjectKnowledgeMapping binds a stored history row to a current object.
// No client instance, provenance, scope or replacement text is accepted. It
// appends a server-stamped copy and retains unmatched history verbatim. Existing
// nonempty user/legacy text wins over AI; equal-origin existing text wins ties.
//
// The service must load the KB and Snapshot under its owner/source gate, then
// persist the returned KB with a repository CAS on the version it read and
// revalidate the current source/selection state. This pure function alone cannot
// make a read/check/write sequence atomic. expectedKBRevision must be the token
// returned by ProjectObjectKnowledge for that current snapshot and KB.
func ConfirmObjectKnowledgeMapping(snapshot schemaobjects.Snapshot, kb *models.KnowledgeBase, expectedKBRevision string, descriptionIndex int, objectID string) (*models.KnowledgeBase, error) {
	projection, err := ProjectObjectKnowledge(snapshot, kb)
	if err != nil {
		return nil, err
	}
	if expectedKBRevision == "" || expectedKBRevision != projection.KBRevision {
		return nil, ErrObjectKnowledgeRevision
	}
	if kb == nil || descriptionIndex < 0 || descriptionIndex >= len(projection.History) {
		return nil, ErrObjectKnowledgeMapping
	}
	source := projection.History[descriptionIndex]
	if source.ObjectScope != nil && !knowledgeSameSource(*source.ObjectScope, snapshot.Scope) {
		return nil, ErrObjectKnowledgeMapping
	}
	var target *schemaobjects.Object
	for i := range snapshot.Objects {
		if snapshot.Objects[i].Metadata.ObjectID == objectID {
			target = &snapshot.Objects[i]
			break
		}
	}
	if target == nil || target.Metadata.InstanceVersion == "" {
		return nil, ErrObjectKnowledgeMapping
	}
	if source.State == models.KnowledgeMapped {
		pending := false
		for _, field := range source.FieldDescriptions {
			pending = pending || field.State == models.KnowledgePending
		}
		if !pending || source.ObjectID != objectID {
			return nil, ErrObjectKnowledgeMapping
		}
	}
	result := *kb
	// History is a current-visibility projection, not persisted association
	// evidence. Preserve all stored rows, including the source row, and append
	// only the newly confirmed copy. Selection/column visibility may recover.
	result.TableDescriptions = cloneKnowledgeDescriptions(kb.TableDescriptions)
	mapped := cloneKnowledgeDescriptions([]models.TableDescription{source})[0]
	scope := snapshot.Scope
	mapped.ObjectID, mapped.InstanceVersion, mapped.ObjectScope = objectID, target.Metadata.InstanceVersion, &scope
	mapped.State = models.KnowledgeMapped
	columns := knowledgeColumnTypes(*target)
	for i := range mapped.FieldDescriptions {
		field := &mapped.FieldDescriptions[i]
		field.State = models.KnowledgePending
		if columnType, ok := columns[field.FieldName]; ok {
			field.ColumnType, field.State = columnType, models.KnowledgeMapped
		}
	}
	// Appending preserves precedence of any already mapped nonempty manual text.
	result.TableDescriptions = append(result.TableDescriptions, mapped)
	updated, err := ProjectObjectKnowledge(snapshot, &result)
	if err != nil {
		return nil, err
	}
	result.KBRevision = updated.KBRevision
	return &result, nil
}

func knowledgeObjects(snapshot schemaobjects.Snapshot) ([]schemaobjects.Object, error) {
	scope := snapshot.Scope
	if !snapshot.Complete || snapshot.MetadataVersion != 2 || scope.OwnerID == "" || scope.ChatID == "" || scope.SourceKey == "" {
		return nil, ErrObjectKnowledgeSnapshot
	}
	objects := make([]schemaobjects.Object, 0, len(snapshot.Objects))
	ids := map[string]bool{}
	for _, object := range snapshot.Objects {
		m := object.Metadata
		if m.Ref.Schema == "" || m.Ref.Name == "" || (m.Ref.Kind != schemaobjects.Table && m.Ref.Kind != schemaobjects.View) ||
			m.ObjectID != schemaobjects.ObjectID(scope.SourceKey, m.Ref) || ids[m.ObjectID] || len(object.Columns) == 0 {
			return nil, ErrObjectKnowledgeSnapshot
		}
		ids[m.ObjectID] = true
		columns := map[string]bool{}
		object.Columns = append([]schemaobjects.Column(nil), object.Columns...)
		for _, column := range object.Columns {
			if column.Name == "" || column.Type == "" || column.Ordinal < 1 || columns[column.Name] {
				return nil, ErrObjectKnowledgeSnapshot
			}
			columns[column.Name] = true
		}
		sort.Slice(object.Columns, func(i, j int) bool {
			if object.Columns[i].Ordinal == object.Columns[j].Ordinal {
				return object.Columns[i].Name < object.Columns[j].Name
			}
			return object.Columns[i].Ordinal < object.Columns[j].Ordinal
		})
		// KB revisions cover current identity/structure/comments, never estimates,
		// registry tombstones for unselected objects, SQL text or clock time.
		object.RowCount, object.RowCountStatus = nil, ""
		// Relationship ordering is already covered by T01's canonical fingerprint.
		object.Constraints, object.Indexes, object.ForeignKeys = nil, nil, nil
		objects = append(objects, object)
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Metadata.ObjectID < objects[j].Metadata.ObjectID })
	return objects, nil
}

func knowledgeSameSource(a, b schemaobjects.Scope) bool {
	return a.OwnerID == b.OwnerID && a.ChatID == b.ChatID && a.SourceKey == b.SourceKey
}

func knowledgeAssociationMatches(row models.TableDescription, object schemaobjects.Object, scope schemaobjects.Scope) bool {
	return row.State == models.KnowledgeMapped && row.ObjectScope != nil &&
		knowledgeSameSource(*row.ObjectScope, scope) &&
		row.ObjectScope.BindingRevision == scope.BindingRevision &&
		row.ObjectScope.ConnectionRevision == scope.ConnectionRevision &&
		row.ObjectID == object.Metadata.ObjectID && row.InstanceVersion != "" &&
		row.InstanceVersion == object.Metadata.InstanceVersion
}

func knowledgeColumnTypes(object schemaobjects.Object) map[string]string {
	result := make(map[string]string, len(object.Columns))
	for _, column := range object.Columns {
		result[column.Name] = column.Type
	}
	return result
}

func cloneKnowledgeDescriptions(rows []models.TableDescription) []models.TableDescription {
	result := make([]models.TableDescription, len(rows))
	for i, row := range rows {
		result[i] = row
		result[i].FieldDescriptions = append([]models.FieldDescription(nil), row.FieldDescriptions...)
		if row.ObjectScope != nil {
			scope := *row.ObjectScope
			result[i].ObjectScope = &scope
		}
	}
	return result
}

func knowledgeProvenance(p models.KnowledgeProvenance) models.KnowledgeProvenance {
	switch p {
	case models.KnowledgeUserEdit, models.KnowledgeAIGenerated, models.KnowledgeDatabaseComment:
		return p
	default:
		return models.KnowledgeLegacyUnknown
	}
}

func preferKnowledgeText(current string, currentOrigin models.KnowledgeProvenance, candidate string, candidateOrigin models.KnowledgeProvenance) bool {
	if strings.TrimSpace(candidate) == "" {
		return false
	}
	if strings.TrimSpace(current) == "" {
		return true
	}
	rank := func(p models.KnowledgeProvenance) int {
		switch p {
		case models.KnowledgeUserEdit:
			return 4
		case models.KnowledgeLegacyUnknown:
			return 3
		case models.KnowledgeDatabaseComment:
			return 2
		case models.KnowledgeAIGenerated:
			return 1
		default:
			return 0
		}
	}
	return rank(candidateOrigin) > rank(currentOrigin)
}

func knowledgeRevisionRows(rows []models.TableDescription) []models.TableDescription {
	result := cloneKnowledgeDescriptions(rows)
	for i := range result {
		// Match Mongo BSON datetime precision/zone so saving cannot invalidate
		// a token solely through encoding. Returned history keeps original times.
		result[i].UpdatedAt = result[i].UpdatedAt.UTC().Truncate(time.Millisecond)
		for j := range result[i].FieldDescriptions {
			field := &result[i].FieldDescriptions[j]
			field.UpdatedAt = field.UpdatedAt.UTC().Truncate(time.Millisecond)
		}
	}
	return result
}

func knowledgeDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ErrObjectKnowledgeData
	}
	sum := sha256.Sum256(encoded)
	return "kb-v1:" + hex.EncodeToString(sum[:]), nil
}
