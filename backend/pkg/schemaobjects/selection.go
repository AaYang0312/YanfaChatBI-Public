package schemaobjects

import (
	"errors"
	"sort"
	"strings"
)

var ErrSelection = errors.New("invalid object selection")

func ValidateSelection(s Selection) error {
	if s.Version != 2 && s.Version != 1 {
		return ErrSelection
	}
	if s.Mode == "all" {
		if len(s.Objects) != 0 || len(s.IncludeKinds) == 0 {
			return ErrSelection
		}
		seen := map[Kind]bool{}
		for _, k := range s.IncludeKinds {
			if (k != Table && k != View) || seen[k] || (s.Version == 1 && k != Table) {
				return ErrSelection
			}
			seen[k] = true
		}
	} else if s.Mode == "explicit" {
		if len(s.Objects) == 0 || len(s.Objects) > 4096 || len(s.IncludeKinds) != 0 {
			return ErrSelection
		}
		seen := map[Ref]bool{}
		for _, r := range s.Objects {
			if !validRef(r, s.Version == 1) || seen[r] || (s.Version == 1 && r.Kind != Table) {
				return ErrSelection
			}
			seen[r] = true
		}
	} else {
		return ErrSelection
	}
	return nil
}
func LegacySelection(value string) (Selection, error) {
	s := Selection{Version: 1, Mode: "all", IncludeKinds: []Kind{Table}}
	if strings.TrimSpace(value) == "" || value == "ALL" {
		return s, nil
	}
	s.Mode = "explicit"
	s.IncludeKinds = nil
	for _, v := range strings.Split(value, ",") {
		p, err := parseName(strings.TrimSpace(v))
		if err != nil {
			return Selection{}, ErrSelection
		}
		r := Ref{Name: p[len(p)-1], Kind: Table}
		if len(p) == 2 {
			r.Schema = p[0]
		}
		s.Objects = append(s.Objects, r)
	}
	return s, ValidateSelection(s)
}

// Select never expands unavailable explicit choices. An absent choice produces a
// smaller/empty projection, while an ambiguous legacy name fails closed.
func Select(objects []Object, s Selection) ([]Object, error) {
	if err := ValidateSelection(s); err != nil {
		return nil, err
	}
	wanted := map[Ref]bool{}
	if s.Mode == "all" {
		for _, o := range objects {
			for _, k := range s.IncludeKinds {
				if o.Metadata.Ref.Kind == k {
					wanted[o.Metadata.Ref] = true
				}
			}
		}
	} else {
		for _, r := range s.Objects {
			if s.Version == 2 || r.Schema != "" {
				wanted[r] = true
				continue
			}
			var matches []Ref
			for _, o := range objects {
				x := o.Metadata.Ref
				if x.Name == r.Name && x.Kind == Table {
					matches = append(matches, x)
				}
			}
			if len(matches) > 1 {
				for _, x := range matches {
					if x.Schema == "public" {
						matches = []Ref{x}
						break
					}
				}
			}
			if len(matches) > 1 {
				return nil, ErrUnavailable
			}
			if len(matches) == 1 {
				wanted[matches[0]] = true
			}
		}
	}
	result := make([]Object, 0)
	allowed := map[Ref]map[string]bool{}
	for _, o := range objects {
		if wanted[o.Metadata.Ref] && len(o.Columns) > 0 {
			o.Columns = append([]Column(nil), o.Columns...)
			allowed[o.Metadata.Ref] = map[string]bool{}
			for _, c := range o.Columns {
				allowed[o.Metadata.Ref][c.Name] = true
			}
			result = append(result, o)
		}
	}
	has := func(cols []string, visible map[string]bool) bool {
		if len(cols) == 0 {
			return false
		}
		for _, c := range cols {
			if !visible[c] {
				return false
			}
		}
		return true
	}
	for i := range result {
		o := &result[i]
		constraints, indexes, fks := o.Constraints, o.Indexes, o.ForeignKeys
		o.Constraints = nil
		o.Indexes = nil
		o.ForeignKeys = nil
		if o.Metadata.Ref.Kind != Table {
			o.RowCount = nil
			o.RowCountStatus = "unknown"
			continue
		}
		visible := allowed[o.Metadata.Ref]
		for _, c := range constraints {
			if has(c.Columns, visible) {
				c.Columns = append([]string(nil), c.Columns...)
				o.Constraints = append(o.Constraints, c)
			}
		}
		for _, x := range indexes {
			if has(x.Columns, visible) {
				x.Columns = append([]string(nil), x.Columns...)
				o.Indexes = append(o.Indexes, x)
			}
		}
		for _, fk := range fks {
			if len(fk.Columns) == len(fk.TargetColumns) && has(fk.Columns, visible) && has(fk.TargetColumns, allowed[fk.Target]) {
				fk.Columns = append([]string(nil), fk.Columns...)
				fk.TargetColumns = append([]string(nil), fk.TargetColumns...)
				o.ForeignKeys = append(o.ForeignKeys, fk)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return refKey(result[i].Metadata.Ref) < refKey(result[j].Metadata.Ref) })
	return result, nil
}
func refKey(r Ref) string { return r.Schema + "\x00" + r.Name + "\x00" + string(r.Kind) }
