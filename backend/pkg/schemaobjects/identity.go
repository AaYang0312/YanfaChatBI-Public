package schemaobjects

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

var ErrUnavailable = errors.New("object unavailable")

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ObjectID(sourceKey string, ref Ref) string {
	return digest([]string{"query-object-v2", sourceKey, ref.Schema, ref.Name, string(ref.Kind)})
}
func SQLIdentifier(ref Ref) string { return quote(ref.Schema) + "." + quote(ref.Name) }
func quote(s string) string        { return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\"" }
func validRef(r Ref, legacy bool) bool {
	return (legacy || r.Schema != "") && r.Name != "" && len(r.Schema) <= 256 && len(r.Name) <= 256 &&
		!strings.ContainsRune(r.Schema+r.Name, 0) && (r.Kind == Table || r.Kind == View)
}

// parseName respects quoted dots and doubled quotes. Unquoted identifiers retain
// case for compatibility with existing display names; no SQL is built from them.
func parseName(s string) ([]string, error) {
	if len(s) == 0 || len(s) > 1024 || strings.ContainsRune(s, 0) {
		return nil, ErrUnavailable
	}
	var parts []string
	var b strings.Builder
	quoted := false
	closed := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quoted {
			if c == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte(c)
					i++
				} else {
					quoted = false
					closed = true
				}
			} else {
				b.WriteByte(c)
			}
		} else {
			switch c {
			case '"':
				if b.Len() != 0 || closed {
					return nil, ErrUnavailable
				}
				quoted = true
			case '.':
				if b.Len() == 0 {
					return nil, ErrUnavailable
				}
				parts = append(parts, b.String())
				b.Reset()
				closed = false
			default:
				if closed {
					return nil, ErrUnavailable
				}
				b.WriteByte(c)
			}
		}
	}
	if quoted || b.Len() == 0 {
		return nil, ErrUnavailable
	}
	parts = append(parts, b.String())
	if len(parts) > 2 {
		return nil, ErrUnavailable
	}
	return parts, nil
}
func Resolve(objects []Object, names []string, refs []Ref) ([]Object, error) {
	if (len(names) > 0 && len(refs) > 0) || len(names)+len(refs) == 0 || len(names)+len(refs) > 256 {
		return nil, ErrUnavailable
	}
	var result []Object
	seen := map[Ref]bool{}
	add := func(o Object) {
		if !seen[o.Metadata.Ref] {
			seen[o.Metadata.Ref] = true
			result = append(result, o)
		}
	}
	for _, r := range refs {
		if !validRef(r, false) {
			return nil, ErrUnavailable
		}
		found := false
		for _, o := range objects {
			if o.Metadata.Ref == r {
				add(o)
				found = true
				break
			}
		}
		if !found {
			return nil, ErrUnavailable
		}
	}
	for _, name := range names {
		parts, err := parseName(name)
		if err != nil {
			return nil, err
		}
		var matches []Object
		for _, o := range objects {
			r := o.Metadata.Ref
			if r.Name == parts[len(parts)-1] && (len(parts) == 1 || r.Schema == parts[0]) {
				matches = append(matches, o)
			}
		}
		if len(parts) == 1 && len(matches) > 1 {
			for _, o := range matches {
				if o.Metadata.Ref.Schema == "public" {
					matches = []Object{o}
					break
				}
			}
		}
		if len(matches) != 1 {
			return nil, ErrUnavailable
		}
		add(matches[0])
	}
	return result, nil
}
