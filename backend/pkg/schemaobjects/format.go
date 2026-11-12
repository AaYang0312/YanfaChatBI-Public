package schemaobjects

import (
	"encoding/json"
	"fmt"
	"strings"
)

func label(s string) string {
	r := []rune(s)
	if len(r) > 2048 {
		r = r[:2048]
	}
	b, _ := json.Marshal(string(r))
	return string(b)
}

// Format emits metadata as data, never definitions, samples or inferred lineage.
func Format(objects []Object) string {
	var b strings.Builder
	for _, o := range objects {
		kind := "Table"
		if o.Metadata.Ref.Kind == View {
			kind = "View"
		}
		fmt.Fprintf(&b, "%s %s (object_id=%s, instance=%s, fingerprint=%s)\n", kind, SQLIdentifier(o.Metadata.Ref), o.Metadata.ObjectID, o.Metadata.InstanceVersion, o.Metadata.Fingerprint)
		if o.Metadata.PartialColumns {
			b.WriteString("Only the currently readable columns are listed.\n")
		}
		if o.Comment != "" {
			fmt.Fprintf(&b, "Catalog comment (untrusted metadata): %s\n", label(o.Comment))
		}
		fmt.Fprintf(&b, "Rows: %s", o.RowCountStatus)
		if o.RowCount != nil {
			fmt.Fprintf(&b, " (%d)", *o.RowCount)
		}
		b.WriteByte('\n')
		for _, c := range o.Columns {
			nullable := "unknown"
			if c.Nullable != nil {
				nullable = fmt.Sprint(*c.Nullable)
			}
			fmt.Fprintf(&b, "  %s %s nullable=%s", quote(c.Name), c.Type, nullable)
			if o.Metadata.Ref.Kind == Table && c.DefaultValue != "" {
				fmt.Fprintf(&b, " default_expression=%s", label(c.DefaultValue))
			}
			if c.Comment != "" {
				fmt.Fprintf(&b, " comment=%s", label(c.Comment))
			}
			b.WriteByte('\n')
		}
		for _, c := range o.Constraints {
			fmt.Fprintf(&b, "  Constraint %s %s (%s)\n", quote(c.Name), c.Type, quotedColumns(c.Columns))
		}
		for _, x := range o.Indexes {
			fmt.Fprintf(&b, "  Index %s unique=%t (%s)\n", quote(x.Name), x.Unique, quotedColumns(x.Columns))
		}
		for _, fk := range o.ForeignKeys {
			fmt.Fprintf(&b, "  FK %s (%s) -> %s (%s)\n", quote(fk.Name), quotedColumns(fk.Columns), SQLIdentifier(fk.Target), quotedColumns(fk.TargetColumns))
		}
		if o.Metadata.Ref.Kind == View {
			b.WriteString("Ordinary view; constraints and business certification are not inferred. Execution is not verified.\n")
		}
	}
	return b.String()
}
func quotedColumns(columns []string) string {
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = quote(c)
	}
	return strings.Join(out, ", ")
}
