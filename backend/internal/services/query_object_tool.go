package services

import (
	"bytes"
	"encoding/json"
	"yanfachatbi/pkg/schemaobjects"
)

// Resolve only against the caller's freshly authorized selection. No argument
// ever changes selection or drives catalog SQL, and unavailable names stay opaque.
func resolveQueryObjectArguments(snapshot schemaobjects.Snapshot, args map[string]interface{}) ([]schemaobjects.Object, error) {
	if !snapshot.Complete || snapshot.MetadataVersion != 2 || len(args) != 1 {
		return nil, schemaobjects.ErrUnavailable
	}
	_, namesPresent := args["table_names"]
	_, refsPresent := args["object_refs"]
	if namesPresent == refsPresent {
		return nil, schemaobjects.ErrUnavailable
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, schemaobjects.ErrUnavailable
	}
	var request struct {
		Names []string            `json:"table_names"`
		Refs  []schemaobjects.Ref `json:"object_refs"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		return nil, schemaobjects.ErrUnavailable
	}
	objects, err := schemaobjects.Resolve(snapshot.Objects, request.Names, request.Refs)
	if err != nil {
		return nil, schemaobjects.ErrUnavailable
	}
	return objects, nil
}
