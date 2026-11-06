package executor

import "strings"

// secretKeyName reports whether a parameter or map-key name looks like it
// holds a credential.
func secretKeyName(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "password") || strings.Contains(k, "secret") ||
		strings.Contains(k, "passwd") || strings.Contains(k, "token")
}

// RedactArgs returns a copy of command arguments that is safe to log or show:
// the value of any "key=value" argument is replaced by "***" when the key looks
// like a credential, or when it is a xenstore-data entry (guest metadata such
// as passwords or cloud-init data that provisioning writes into a VM).
// Arguments without "=" are returned unchanged.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a
		eq := strings.Index(a, "=")
		if eq <= 0 {
			continue
		}
		key := a[:eq]
		if secretKeyName(key) || strings.HasPrefix(strings.ToLower(key), "xenstore-data:") {
			if eq+1 < len(a) { // leave an empty value visible
				out[i] = key + "=***"
			}
		}
	}
	return out
}
