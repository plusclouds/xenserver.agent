package xenserver

import (
	"fmt"
	"strings"
)

// parseXEBlocks parses xe's verbose "<class>-list"/"<class>-param-list" block
// output into one map[string]string per record. Records are separated by
// blank lines; each line has the shape "key ( RO/RW/MRO/...)  : value". The
// key's trailing "( ... )" annotation is stripped; the value is everything
// after the first colon, verbatim (so values containing colons, such as
// IPv6 addresses, are preserved intact).
func parseXEBlocks(output string) []map[string]string {
	var blocks []map[string]string
	var current map[string]string

	flush := func() {
		if len(current) > 0 {
			blocks = append(blocks, current)
		}
		current = nil
	}

	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}

		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := stripAnnotation(line[:idx])
		if key == "" {
			continue
		}
		if current == nil {
			current = make(map[string]string)
		}
		current[key] = strings.TrimSpace(line[idx+1:])
	}
	flush()

	return blocks
}

// stripAnnotation removes the trailing "( RO)"/"( RW)"/"( MRO)" style
// annotation xe attaches to every field name and trims whitespace.
func stripAnnotation(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.LastIndex(s, "("); i >= 0 && strings.HasSuffix(s, ")") {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// pickUUID resolves a name-label (or other filter) to exactly one uuid from
// the comma-separated output of "xe <class>-list <filter> --minimal". It
// errors clearly on zero or multiple matches — name-label is not unique in
// XenServer, so ambiguity must be surfaced rather than silently resolved.
func pickUUID(minimalOutput, filterDescription string) (string, error) {
	uuids := splitMinimalList(minimalOutput)
	if len(uuids) == 0 {
		return "", fmt.Errorf("no object found matching %s", filterDescription)
	}
	if len(uuids) > 1 {
		return "", fmt.Errorf("ambiguous filter %s matches %d objects: %s",
			filterDescription, len(uuids), strings.Join(uuids, ", "))
	}
	return uuids[0], nil
}

// splitMinimalList splits the comma-separated uuid list produced by
// "xe <class>-list ... --minimal" into individual, trimmed uuids.
func splitMinimalList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitSetField splits the semicolon-separated display form xe uses for
// string-set fields (e.g. "10.0.0.1; 10.0.0.2") into individual, trimmed
// values.
func splitSetField(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
