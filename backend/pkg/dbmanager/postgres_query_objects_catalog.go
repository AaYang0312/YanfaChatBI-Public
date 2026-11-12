package dbmanager

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"yanfachatbi/pkg/schemaobjects"
)

// catalogQueryer deliberately needs no pool access: postgresSession retains and
// checks the source permit for every query and until all rows have closed.
type catalogQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Only persistent ordinary/partitioned tables and ordinary views. A namespace
// privilege and at least one readable output column are required. No base-table
// access is needed to inspect an ordinary view's own pg_attribute rows.
const queryObjectsColumnsSQL = `/* query_objects_columns_v2 */
SELECT c.oid, n.nspname, c.relname, c.relkind::text,
 COALESCE(pg_catalog.obj_description(c.oid, 'pg_class'), ''),
 a.attname, pg_catalog.format_type(a.atttypid,a.atttypmod), a.attnum,
 a.attnotnull, t.typtype::text, t.typnotnull,
 COALESCE(pg_catalog.col_description(c.oid,a.attnum), ''),
 CASE WHEN c.relkind IN ('r','p') AND a.attgenerated = ''
  THEN COALESCE(pg_catalog.pg_get_expr(ad.adbin,ad.adrelid),'') ELSE '' END,
 EXISTS (SELECT 1 FROM pg_catalog.pg_attribute hidden
  WHERE hidden.attrelid=c.oid AND hidden.attnum>0 AND NOT hidden.attisdropped
   AND NOT pg_catalog.has_column_privilege(c.oid,hidden.attnum,'SELECT'))
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid
JOIN pg_catalog.pg_type t ON t.oid=a.atttypid
LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid=a.attrelid AND ad.adnum=a.attnum
WHERE c.relkind IN ($1,$2,$3) AND c.relpersistence <> 't'
 AND n.nspname <> 'information_schema' AND n.nspname !~ '^pg_'
 AND pg_catalog.has_schema_privilege(n.oid,'USAGE')
 AND a.attnum>0 AND NOT a.attisdropped
 AND pg_catalog.has_column_privilege(c.oid,a.attnum,'SELECT')
ORDER BY n.nspname,c.relname,a.attnum`

const queryObjectsConstraintsSQL = `/* query_objects_constraints_v2 */
SELECT c.oid, k.conname, k.contype::text,
 (SELECT json_agg(a.attname ORDER BY x.ord)::text
  FROM unnest(k.conkey) WITH ORDINALITY x(num,ord)
  LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=k.conrelid AND a.attnum=x.num AND NOT a.attisdropped),
 COALESCE(tn.nspname,''),COALESCE(t.relname,''),
 COALESCE((SELECT json_agg(a.attname ORDER BY x.ord)::text
  FROM unnest(k.confkey) WITH ORDINALITY x(num,ord)
  LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=k.confrelid AND a.attnum=x.num AND NOT a.attisdropped),'[]'),
 CASE k.confdeltype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' ELSE '' END,
 CASE k.confupdtype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' ELSE '' END
FROM pg_catalog.pg_constraint k
JOIN pg_catalog.pg_class c ON c.oid=k.conrelid
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
LEFT JOIN pg_catalog.pg_class t ON t.oid=k.confrelid
LEFT JOIN pg_catalog.pg_namespace tn ON tn.oid=t.relnamespace
WHERE c.relkind IN ('r','p') AND c.relpersistence <> 't'
 AND n.nspname <> 'information_schema' AND n.nspname !~ '^pg_'
 AND pg_catalog.has_schema_privilege(n.oid,'USAGE')
 AND pg_catalog.has_any_column_privilege(c.oid,'SELECT')
 AND k.contype IN ('p','u','f') AND k.convalidated
ORDER BY c.oid,k.conname`

// Expression and partial indexes are omitted completely: a list of plain
// columns cannot represent their semantics. INCLUDE attributes participate in
// the visibility check, but only key attributes describe uniqueness/order.
// See PostgreSQL 18 pg_index: indkey, indnkeyatts, indexprs, indpred.
const queryObjectsIndexesSQL = `/* query_objects_indexes_v2 */
SELECT c.oid, ic.relname,
 (SELECT json_agg(a.attname ORDER BY x.ord)::text
  FROM unnest(i.indkey) WITH ORDINALITY x(num,ord)
  JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=x.num
  WHERE x.ord <= i.indnkeyatts), i.indisunique
FROM pg_catalog.pg_index i
JOIN pg_catalog.pg_class c ON c.oid=i.indrelid
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
JOIN pg_catalog.pg_class ic ON ic.oid=i.indexrelid
WHERE c.relkind IN ('r','p') AND c.relpersistence <> 't'
 AND n.nspname <> 'information_schema' AND n.nspname !~ '^pg_'
 AND pg_catalog.has_schema_privilege(n.oid,'USAGE')
 AND i.indexprs IS NULL AND i.indpred IS NULL
 AND i.indisvalid AND i.indisready AND i.indislive AND i.indnkeyatts>0
 AND NOT EXISTS (SELECT 1 FROM unnest(i.indkey) x(num)
  LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=x.num
  WHERE a.attnum IS NULL OR a.attisdropped OR x.num<=0
   OR NOT pg_catalog.has_column_privilege(c.oid,a.attnum,'SELECT'))
ORDER BY c.oid,ic.relname`

// information_schema deliberately redacts view_definition unless the current
// reader can see it. Never substitute pg_get_viewdef to defeat that redaction.
const queryObjectsDefinitionsSQL = `/* query_objects_definitions_v2 */
SELECT v.table_schema,v.table_name,v.view_definition
FROM information_schema.views v
JOIN pg_catalog.pg_namespace n ON n.nspname=v.table_schema
WHERE n.nspname <> 'information_schema' AND n.nspname !~ '^pg_'
 AND pg_catalog.has_schema_privilege(n.oid,'USAGE')`

func readQueryObjectsCatalog(ctx context.Context, db catalogQueryer) ([]schemaobjects.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	objects := []schemaobjects.Object{}
	byOID := map[uint32]int{}
	err := catalogRowsEach(ctx, db, queryObjectsColumnsSQL, func(rows *sql.Rows) error {
		var oid uint32
		var ns, name, kind, comment, col, typ, colComment string
		var ordinal int
		var attributeNotNull, typeNotNull bool
		var typeKind, defaultValue string
		var partial bool
		if err := rows.Scan(&oid, &ns, &name, &kind, &comment, &col, &typ, &ordinal, &attributeNotNull, &typeKind, &typeNotNull, &colComment, &defaultValue, &partial); err != nil {
			return err
		}
		if oid == 0 || ns == "" || name == "" || col == "" || typ == "" || ordinal < 1 {
			return fmt.Errorf("incomplete catalog column")
		}
		k := schemaobjects.Table
		if kind == "v" {
			k = schemaobjects.View
		} else if kind != "r" && kind != "p" {
			return fmt.Errorf("unsupported catalog kind")
		}
		pos, ok := byOID[oid]
		if !ok {
			status := "not_collected"
			if k == schemaobjects.View {
				status = "unavailable"
			}
			objects = append(objects, schemaobjects.Object{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: ns, Name: name, Kind: k}, CatalogOID: oid, PartialColumns: partial, DefinitionStatus: status}, Comment: comment, RowCountStatus: "unknown"})
			pos = len(objects) - 1
			byOID[oid] = pos
		}
		o := &objects[pos]
		if o.Metadata.Ref != (schemaobjects.Ref{Schema: ns, Name: name, Kind: k}) {
			return fmt.Errorf("inconsistent catalog identity")
		}
		for _, existing := range o.Columns {
			if existing.Name == col || existing.Ordinal == ordinal {
				return fmt.Errorf("duplicate catalog column")
			}
		}
		n := postgresColumnNullable(kind, attributeNotNull, typeKind, typeNotNull)
		if k == schemaobjects.View {
			defaultValue = ""
		}
		o.Columns = append(o.Columns, schemaobjects.Column{Name: col, Type: typ, Ordinal: ordinal, Nullable: n, Comment: colComment, DefaultValue: defaultValue})
		return nil
	}, "r", "p", "v")
	if err != nil {
		return nil, fmt.Errorf("query object columns: %w", err)
	}
	err = catalogRowsEach(ctx, db, queryObjectsConstraintsSQL, func(rows *sql.Rows) error {
		var oid uint32
		var name, kind, cols, ns, target, targetCols, onDelete, onUpdate string
		if err := rows.Scan(&oid, &name, &kind, &cols, &ns, &target, &targetCols, &onDelete, &onUpdate); err != nil {
			return err
		}
		pos, ok := byOID[oid]
		if !ok {
			return nil
		}
		o := &objects[pos]
		var columns, targets []string
		if err := json.Unmarshal([]byte(cols), &columns); err != nil {
			return err
		}
		if kind == "f" {
			if err := json.Unmarshal([]byte(targetCols), &targets); err != nil {
				return err
			}
			o.ForeignKeys = append(o.ForeignKeys, schemaobjects.ForeignKey{Name: name, Columns: columns, Target: schemaobjects.Ref{Schema: ns, Name: target, Kind: schemaobjects.Table}, TargetColumns: targets, OnDelete: onDelete, OnUpdate: onUpdate})
		} else if kind == "p" || kind == "u" {
			typ := "PRIMARY KEY"
			if kind == "u" {
				typ = "UNIQUE"
			}
			o.Constraints = append(o.Constraints, schemaobjects.Constraint{Name: name, Type: typ, Columns: columns})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("query object constraints: %w", err)
	}
	err = catalogRowsEach(ctx, db, queryObjectsIndexesSQL, func(rows *sql.Rows) error {
		var oid uint32
		var name, cols string
		var unique bool
		if err := rows.Scan(&oid, &name, &cols, &unique); err != nil {
			return err
		}
		pos, ok := byOID[oid]
		if !ok {
			return nil
		}
		var columns []string
		if err := json.Unmarshal([]byte(cols), &columns); err != nil {
			return err
		}
		objects[pos].Indexes = append(objects[pos].Indexes, schemaobjects.Index{Name: name, Columns: columns, Unique: unique})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("query object indexes: %w", err)
	}
	definitions := map[schemaobjects.Ref]string{}
	err = catalogRowsEach(ctx, db, queryObjectsDefinitionsSQL, func(rows *sql.Rows) error {
		var ns, name string
		var definition sql.NullString
		if err := rows.Scan(&ns, &name, &definition); err != nil {
			return err
		}
		if definition.Valid {
			sum := sha256.Sum256([]byte(definition.String))
			definitions[schemaobjects.Ref{Schema: ns, Name: name, Kind: schemaobjects.View}] = hex.EncodeToString(sum[:])
		}
		return nil
	})
	// Definition failures do not turn successfully collected output columns into
	// missing objects. Do not publish partially read definition digests either.
	if err == nil {
		for i := range objects {
			if sum, ok := definitions[objects[i].Metadata.Ref]; ok {
				objects[i].Metadata.DefinitionStatus = "visible"
				objects[i].Metadata.DefinitionFingerprint = sum
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return schemaobjects.Select(objects, schemaobjects.Selection{Version: 2, Mode: "all", IncludeKinds: []schemaobjects.Kind{schemaobjects.Table, schemaobjects.View}})
}

func catalogRowsEach(ctx context.Context, db catalogQueryer, query string, scan func(*sql.Rows) error, args ...any) (err error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
	}()
	for rows.Next() {
		if err = scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Match information_schema.columns: attribute NOT NULL or domain NOT NULL.
// Views retain unknown output nullability and do not inherit base-table facts.
func postgresColumnNullable(kind string, attributeNotNull bool, typeKind string, typeNotNull bool) *bool {
	if kind == "v" {
		return nil
	}
	nullable := !(attributeNotNull || (typeKind == "d" && typeNotNull))
	return &nullable
}
