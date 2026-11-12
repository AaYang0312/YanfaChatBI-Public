package models

import "yanfachatbi/pkg/schemaobjects"

// State is held in the Chat document, so snapshot and index publication share
// the same database CAS boundary as connection and selection revisions.
type State struct {
	Revision int64                  `bson:"revision" json:"revision"`
	Scope    schemaobjects.Scope    `bson:"scope" json:"scope"`
	Registry schemaobjects.Registry `bson:"registry" json:"-"`
	Manifest Manifest               `bson:"manifest" json:"manifest"`
}
type Manifest struct {
	FormatVersion      int                 `bson:"format_version" json:"format_version"`
	Status             string              `bson:"status" json:"status"`
	ActiveGeneration   string              `bson:"active_generation" json:"active_generation,omitempty"`
	BuildingGeneration string              `bson:"building_generation" json:"building_generation,omitempty"`
	FailureCode        string              `bson:"failure_code" json:"failure_code,omitempty"`
	Scope              schemaobjects.Scope `bson:"scope" json:"-"`
	SchemaFingerprint  string              `bson:"schema_fingerprint" json:"schema_fingerprint"`
	KBRevision         string              `bson:"kb_revision" json:"kb_revision"`
	ExpectedPoints     int                 `bson:"expected_points" json:"expected_points"`
}

func (c *Chat) QuerySelection() (schemaobjects.Selection, error) {
	if c.SelectedObjects == nil {
		return schemaobjects.LegacySelection(c.SelectedCollections)
	}
	s := *c.SelectedObjects
	if s.Version != 2 {
		return schemaobjects.Selection{}, schemaobjects.ErrSelection
	}
	if err := schemaobjects.ValidateSelection(s); err != nil {
		return schemaobjects.Selection{}, err
	}
	s.Objects = append([]schemaobjects.Ref(nil), s.Objects...)
	s.IncludeKinds = append([]schemaobjects.Kind(nil), s.IncludeKinds...)
	return s, nil
}
