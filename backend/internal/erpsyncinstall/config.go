package erpsyncinstall

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"unicode/utf8"
)

var ErrConfig = errors.New("ERP_CONFIG_LITERAL_INVALID")
var EnvironmentKeys = []string{"BI_WRITER_DSN", "BI_SHOP_IDS", "KUAI_MAI_APP_KEY", "KUAI_MAI_APP_SECRET", "KUAI_MAI_ACCESS_TOKEN", "KUAI_MAI_REFRESH_TOKEN"}

type ConvertRequest struct {
	Source           string `json:"source"`
	Destination      string `json:"destination"`
	UID              uint32 `json:"uid"`
	AuthorizationRef string `json:"authorization_ref"`
}

func ParseDotenv(raw []byte) (map[string]string, error) {
	if len(raw) > 64<<10 || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, ErrConfig
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, ErrConfig
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		known := false
		for _, allowed := range EnvironmentKeys {
			if key == allowed {
				known = true
			}
		}
		if !known || values[key] != "" || value == "" {
			return nil, ErrConfig
		}
		if value[0] == '\'' || value[0] == '"' {
			quote := value[0]
			if len(value) < 2 || value[len(value)-1] != quote {
				return nil, ErrConfig
			}
			value = value[1 : len(value)-1]
			if strings.ContainsRune(value, rune(quote)) || strings.ContainsAny(value, "\r\n") {
				return nil, ErrConfig
			}
			if quote == '"' && strings.ContainsAny(value, "$`\\") {
				return nil, ErrConfig
			}
		} else {
			if strings.ContainsAny(value, " \t\r\n'\"\\$`;|&()<>#") {
				return nil, ErrConfig
			}
		}
		if value == "" {
			return nil, ErrConfig
		}
		values[key] = value
	}
	if len(values) != len(EnvironmentKeys) {
		return nil, ErrConfig
	}
	return values, nil
}
func ConvertConfig(req ConvertRequest) error {
	if req.AuthorizationRef == "" || req.UID != uint32(os.Geteuid()) || req.Source == req.Destination {
		return ErrConfig
	}
	f, err := OpenFile(req.Source, req.UID, 0600, false)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := ReadFile(req.Source, req.UID, 64<<10)
	if err != nil {
		return err
	}
	values, err := ParseDotenv(raw)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return ErrConfig
	}
	return WriteExclusive(req.Destination, append(encoded, '\n'), 0600)
}
