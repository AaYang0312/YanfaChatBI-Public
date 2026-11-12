package services

import "yanfachatbi/pkg/schemaobjects"

// Registry continuity is based on the successfully collected readable catalog;
// relationship disclosure and fingerprints are based on the selected projection.
func projectQueryCatalog(scope schemaobjects.Scope, previous schemaobjects.Registry, catalog []schemaobjects.Object, selection schemaobjects.Selection) (*schemaobjects.Snapshot, error) {
	full, err := schemaobjects.Reconcile(scope, previous, catalog)
	if err != nil {
		return nil, err
	}
	selected, err := schemaobjects.Select(full.Objects, selection)
	if err != nil {
		return nil, err
	}
	projected, err := schemaobjects.Reconcile(scope, full.Registry, selected)
	if err != nil {
		return nil, err
	}
	projected.Registry = full.Registry
	return &projected, nil
}
