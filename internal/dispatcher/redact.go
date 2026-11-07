package dispatcher

import (
	"encoding/json"
	"fmt"
	"strings"
)

// redactParams masks credentials in a command's params JSON before it is
// logged: any value whose key looks secret (password, secret, passwd, token)
// and every "xenstore-data:<key>" value (guest metadata such as passwords or
// cloud-init data). Params that are not a JSON object are not logged verbatim.
func redactParams(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return []byte(`"<unparseable params>"`)
	}
	out, err := json.Marshal(redactValue(v))
	if err != nil {
		return []byte(`"<unparseable params>"`)
	}
	return out
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			lk := strings.ToLower(k)
			secret := strings.Contains(lk, "password") || strings.Contains(lk, "secret") ||
				strings.Contains(lk, "passwd") || strings.Contains(lk, "token") ||
				strings.HasPrefix(lk, "xenstore-data:")
			if lk == "files" {
				// Config-drive file contents (user-data may hold passwords): log sizes only.
				if fm, ok := val.(map[string]any); ok {
					for name, c := range fm {
						if cs, ok := c.(string); ok {
							fm[name] = fmt.Sprintf("<%d bytes>", len(cs))
						}
					}
					t[k] = fm
					continue
				}
			}
			if s, ok := val.(string); ok && secret && s != "" {
				t[k] = "***"
				continue
			}
			t[k] = redactValue(val)
		}
		return t
	case []any:
		for i := range t {
			t[i] = redactValue(t[i])
		}
		return t
	}
	return v
}
