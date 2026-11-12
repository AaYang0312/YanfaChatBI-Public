package services

import "yanfachatbi/internal/models"

func objectIndexStatus(chat *models.Chat) *models.Manifest {
	if chat.ObjectState == nil {
		return nil
	}
	manifest := chat.ObjectState.Manifest
	if manifest.Scope.SelectionRevision != chat.SelectionRevision ||
		manifest.Scope.BindingRevision != chat.SourceBindingRevision ||
		manifest.Scope.ConnectionRevision != chat.ConnectionRevision {
		manifest.Status = "stale"
		manifest.ActiveGeneration, manifest.BuildingGeneration = "", ""
	}
	return &manifest
}
