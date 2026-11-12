package schemaobjects

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
)

func newVersion() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Reconcile accepts only a successfully collected catalog. Collection failures
// must return before calling it; an empty successful catalog records tombstones.
func Reconcile(scope Scope, previous Registry, objects []Object) (Snapshot, error) {
	if scope.SourceKey == "" || scope.OwnerID == "" || scope.ChatID == "" {
		return Snapshot{}, errors.New("invalid object scope")
	}
	s := Snapshot{Scope: scope, MetadataVersion: 2, Registry: Registry{}, Objects: []Object{}}
	for id, instance := range previous {
		instance.Present = false
		s.Registry[id] = instance
	}
	seen := map[Ref]bool{}
	for _, o := range objects {
		if !validRef(o.Metadata.Ref, false) || len(o.Columns) == 0 || seen[o.Metadata.Ref] {
			return Snapshot{}, errors.New("incomplete object catalog")
		}
		seen[o.Metadata.Ref] = true
		o.Columns = append([]Column(nil), o.Columns...)
		sort.Slice(o.Columns, func(i, j int) bool {
			if o.Columns[i].Ordinal == o.Columns[j].Ordinal {
				return o.Columns[i].Name < o.Columns[j].Name
			}
			return o.Columns[i].Ordinal < o.Columns[j].Ordinal
		})
		colSeen := map[string]bool{}
		for _, c := range o.Columns {
			if c.Name == "" || c.Type == "" || c.Ordinal < 1 || colSeen[c.Name] {
				return Snapshot{}, errors.New("incomplete object columns")
			}
			colSeen[c.Name] = true
		}
		id := ObjectID(scope.SourceKey, o.Metadata.Ref)
		old := previous[id]
		version := ""
		if o.Metadata.CatalogOID != 0 {
			if old.Present && old.CatalogOID == o.Metadata.CatalogOID && old.Version != "" {
				version = old.Version
			} else {
				var err error
				version, err = newVersion()
				if err != nil {
					return Snapshot{}, err
				}
			}
		}
		o.Metadata.ObjectID = id
		o.Metadata.InstanceVersion = version
		s.Registry[id] = Instance{CatalogOID: o.Metadata.CatalogOID, Version: version, Present: true}
		s.Objects = append(s.Objects, o)
	}
	// Sanitize all relationship evidence even if callers forgot Select.
	var err error
	s.Objects, err = Select(s.Objects, Selection{Version: 2, Mode: "all", IncludeKinds: []Kind{Table, View}})
	if err != nil {
		return Snapshot{}, err
	}
	identities := []string{}
	for i := range s.Objects {
		o := &s.Objects[i]
		o.Constraints = append([]Constraint(nil), o.Constraints...)
		o.Indexes = append([]Index(nil), o.Indexes...)
		o.ForeignKeys = append([]ForeignKey(nil), o.ForeignKeys...)
		sort.Slice(o.Constraints, func(i, j int) bool { return digest(o.Constraints[i]) < digest(o.Constraints[j]) })
		sort.Slice(o.Indexes, func(i, j int) bool { return digest(o.Indexes[i]) < digest(o.Indexes[j]) })
		sort.Slice(o.ForeignKeys, func(i, j int) bool { return digest(o.ForeignKeys[i]) < digest(o.ForeignKeys[j]) })
		structural := *o
		structural.Metadata.Fingerprint = ""
		structural.RowCount = nil
		structural.RowCountStatus = ""
		o.Metadata.Fingerprint = digest(structural)
		identities = append(identities, o.Metadata.ObjectID+":"+o.Metadata.Fingerprint)
	}
	s.Fingerprint = digest(struct {
		Version int
		Scope   Scope
		Objects []string
	}{2, scope, identities})
	s.Complete = true
	return s, nil
}
