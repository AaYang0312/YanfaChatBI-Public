package schemaobjects

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func defaultObject(t *testing.T, expr string) Object {
	t.Helper()
	raw := `{"metadata":{"ref":{"schema":"public","name":"quantity","kind":"table"},"catalog_oid":10},"columns":[{"name":"amount","type":"integer","ordinal":1,"nullable":false,"default_value":EXPR}],"row_count_status":"unknown"}`
	value, _ := json.Marshal(expr)
	var o Object
	if err := json.Unmarshal([]byte(strings.Replace(raw, "EXPR", string(value), 1)), &o); err != nil {
		t.Fatal(err)
	}
	return o
}
func TestColumnDefaultWireAndFingerprint(t *testing.T) {
	one := defaultObject(t, "1")
	two := defaultObject(t, "2")
	scope := Scope{OwnerID: "owner", ChatID: "chat", SourceKey: "source"}
	a, err := Reconcile(scope, nil, []Object{one})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Reconcile(scope, a.Registry, []Object{two})
	if err != nil {
		t.Fatal(err)
	}
	if a.Objects[0].Metadata.Fingerprint == b.Objects[0].Metadata.Fingerprint || a.Fingerprint == b.Fingerprint {
		t.Fatal("default-only change did not invalidate fingerprint")
	}
	raw, _ := json.Marshal(one.Columns[0])
	if !strings.Contains(string(raw), `"default_value":"1"`) {
		t.Fatal("default value lost in JSON", string(raw))
	}
	f, ok := reflect.TypeOf(Column{}).FieldByName("DefaultValue")
	if !ok || f.Tag.Get("json") != "default_value,omitempty" || f.Tag.Get("bson") != "default_value,omitempty" {
		t.Fatal("default wire tags missing")
	}
}
func TestColumnDefaultFormattedAsQuotedMetadata(t *testing.T) {
	o := defaultObject(t, "nextval('quantity_seq'::regclass)\n\"untrusted\"")
	text := Format([]Object{o})
	if !strings.Contains(text, `default_expression="nextval('quantity_seq'::regclass)\n\"untrusted\""`) {
		t.Fatal("default not quoted metadata", text)
	}
	o.Metadata.Ref.Kind = View
	if strings.Contains(Format([]Object{o}), "default_expression") || strings.Contains(Format([]Object{o}), "quantity_seq") {
		t.Fatal("view inherited default")
	}
}
