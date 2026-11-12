package dbmanager

import (
	"go.mongodb.org/mongo-driver/bson"
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

func TestSchemaObjectsColumnDefaultBSON(t *testing.T) {
	for _, value := range []string{"", "nextval('quantity_seq'::regclass)"} {
		c := schemaobjects.Column{Name: "quantity", Type: "int", Ordinal: 1, DefaultValue: value}
		raw, err := bson.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var decoded bson.M
		if err = bson.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		actual, present := decoded["default_value"]
		if value == "" {
			if present {
				t.Fatal("empty default not omitted")
			}
		} else if actual != value {
			t.Fatal("BSON default lost", decoded)
		}
		var round schemaobjects.Column
		if err = bson.Unmarshal(raw, &round); err != nil || round.DefaultValue != value {
			t.Fatal("BSON roundtrip lost", err)
		}
	}
}
