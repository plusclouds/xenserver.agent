package xenserver

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// VMMetric is one VM's latest RRD-derived utilisation snapshot — actual
// observed usage (CPU, memory, vif/vbd throughput, ...) as reported by
// XAPI's own performance monitor, not the static allocation figures
// VMList/VMGet return.
type VMMetric struct {
	UUID    string             `json:"uuid"`
	Metrics map[string]float64 `json:"metrics"`
}

// HostMetric is the host's own latest RRD-derived utilisation snapshot.
type HostMetric struct {
	UUID    string             `json:"uuid"`
	Metrics map[string]float64 `json:"metrics"`
}

// RRDSnapshot is one parsed rrd_updates response: the most recent sample
// for the host and every VM XAPI is tracking (including the control
// domain, which XAPI reports as a VM).
type RRDSnapshot struct {
	Host    *HostMetric `json:"host,omitempty"`
	VMs     []VMMetric  `json:"vms,omitempty"`
	EndTime int64       `json:"end_time"`
}

// xportDoc mirrors the XML shape XAPI's rrd_updates HTTP endpoint returns —
// the standard RRDtool "xport" export format.
//
// Flagged for validation against real infrastructure: this is the
// well-known RRDtool xport DTD (xport/meta/legend/entry, xport/data/row),
// which the events.py/rrd.py sidecar scripts this replaces relied on
// positionally rather than by tag — confirm tag names against a real host.
type xportDoc struct {
	XMLName xml.Name  `xml:"xport"`
	Meta    xportMeta `xml:"meta"`
	Data    xportData `xml:"data"`
}

type xportMeta struct {
	Start   int64       `xml:"start"`
	Step    int64       `xml:"step"`
	End     int64       `xml:"end"`
	Rows    int         `xml:"rows"`
	Columns int         `xml:"columns"`
	Legend  xportLegend `xml:"legend"`
}

type xportLegend struct {
	Entries []string `xml:"entry"`
}

type xportData struct {
	Rows []xportRow `xml:"row"`
}

type xportRow struct {
	T int64     `xml:"t"`
	V []float64 `xml:"v"`
}

// RRDUpdates fetches the latest per-host/per-VM utilisation sample from
// XAPI's RRD interface (replaces the rrd.py sidecar script). since is a
// Unix timestamp for the start of the query window; pass 0 to default to a
// 3-minute lookback. Only the newest sample in the window is returned —
// callers wanting a continuous series should track EndTime and pass
// EndTime+1 as since on the next call, same as the original script did.
func (m *Module) RRDUpdates(ctx context.Context, since int64) (*RRDSnapshot, error) {
	client := newXAPIHTTPClient(m.xapiSocketPath)

	sessionRef, err := xapiLogin(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("rrd_updates: %w", err)
	}
	defer xapiLogout(context.Background(), client, sessionRef)

	if since <= 0 {
		since = time.Now().Add(-3 * time.Minute).Unix()
	}

	url := fmt.Sprintf(
		"http://localhost/rrd_updates?session_id=%s&start=%d&host=true&cf=AVERAGE&interval=60",
		sessionRef, since,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building rrd_updates request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rrd_updates request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("rrd_updates returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading rrd_updates response: %w", err)
	}

	return parseRRDXPort(body)
}

// parseRRDXPort parses an RRDtool xport document and extracts the newest
// (first) row into per-object metric maps. The legend describes each
// column as "cf:vm|host:uuid:param"; rows are reverse-chronological, so
// the first <row> is the most recent sample.
func parseRRDXPort(data []byte) (*RRDSnapshot, error) {
	var doc xportDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing rrd xport xml: %w", err)
	}

	snapshot := &RRDSnapshot{EndTime: doc.Meta.End}
	if len(doc.Data.Rows) == 0 {
		return snapshot, nil
	}
	latest := doc.Data.Rows[0]

	type objKey struct {
		kind string
		uuid string
	}
	byObject := make(map[objKey]map[string]float64)
	var order []objKey

	for col, entry := range doc.Meta.Legend.Entries {
		if col >= len(latest.V) {
			continue
		}
		parts := strings.SplitN(entry, ":", 4)
		if len(parts) != 4 {
			continue
		}
		kind, uuid, param := parts[1], parts[2], parts[3]
		k := objKey{kind: kind, uuid: uuid}
		if byObject[k] == nil {
			byObject[k] = make(map[string]float64)
			order = append(order, k)
		}
		byObject[k][param] = latest.V[col]
	}

	for _, k := range order {
		switch k.kind {
		case "host":
			snapshot.Host = &HostMetric{UUID: k.uuid, Metrics: byObject[k]}
		case "vm":
			snapshot.VMs = append(snapshot.VMs, VMMetric{UUID: k.uuid, Metrics: byObject[k]})
		}
	}

	return snapshot, nil
}

// FilterRRD narrows a snapshot to one VM: with vmUUID set, only that VM's
// metrics are returned and the (large) host block is dropped.
func FilterRRD(s *RRDSnapshot, vmUUID string) *RRDSnapshot {
	if s == nil || vmUUID == "" {
		return s
	}
	out := &RRDSnapshot{EndTime: s.EndTime}
	for _, v := range s.VMs {
		if v.UUID == vmUUID {
			out.VMs = append(out.VMs, v)
		}
	}
	return out
}
