package xenserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// XAPIEvent is one entry from XenAPI's event.from long-poll: a notification
// that some XAPI object (VM, SR, VBD, host, ...) changed.
type XAPIEvent struct {
	ID        string `json:"id"`
	Class     string `json:"class"`
	Operation string `json:"operation"`
	Ref       string `json:"ref"`
	ObjUUID   string `json:"obj_uuid"`
	// Snapshot is the object's full record at the time of the event, exactly
	// as XAPI returned it — kept as a raw blob rather than decomposed, since
	// its shape varies per class.
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
}

// eventFromResult mirrors XenAPI's event.from return shape.
type eventFromResult struct {
	Token  string      `json:"token"`
	Events []xapiEvent `json:"events"`
}

type xapiEvent struct {
	ID        string          `json:"id"`
	Class     string          `json:"class"`
	Operation string          `json:"operation"`
	Ref       string          `json:"ref"`
	ObjUUID   string          `json:"obj_uuid"`
	Snapshot  json.RawMessage `json:"snapshot"`
}

// EventPoller holds a long-lived xapi session for repeated event.from calls.
// Unlike VDIExport's one-shot login/logout, polling reuses a single session
// across many calls rather than re-authenticating every time. Replaces the
// events.py sidecar script (session.xenapi.event_from loop).
type EventPoller struct {
	client     *http.Client
	sessionRef string
}

// NewEventPoller logs into xapi over the local Unix socket and returns a
// poller ready for repeated Poll calls. Call Close when done.
func (m *Module) NewEventPoller(ctx context.Context) (*EventPoller, error) {
	client := newXAPIHTTPClient(m.xapiSocketPath)
	sessionRef, err := xapiLogin(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("starting xapi event session: %w", err)
	}
	return &EventPoller{client: client, sessionRef: sessionRef}, nil
}

// Close releases the poller's xapi session. Best-effort.
func (p *EventPoller) Close(ctx context.Context) {
	xapiLogout(ctx, p.client, p.sessionRef)
}

// Poll calls event.from once for every object class, blocking up to
// timeoutS seconds server-side for new events. token should be "" on the
// first call and the previously-returned token thereafter, so only events
// since the last call are returned.
func (p *EventPoller) Poll(ctx context.Context, token string, timeoutS float64) (events []XAPIEvent, nextToken string, err error) {
	// The server-side long-poll can legitimately take up to timeoutS; give
	// the request itself a bit of headroom beyond that rather than reusing
	// ctx's own deadline (if any), so a slow-but-healthy poll isn't cut off.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second+10*time.Second)
	defer cancel()

	result, err := callXAPIMethod(ctx, p.client, "event.from",
		[]interface{}{p.sessionRef, []string{"*"}, token, timeoutS})
	if err != nil {
		return nil, token, fmt.Errorf("event.from: %w", err)
	}

	var parsed eventFromResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		return nil, token, fmt.Errorf("parsing event.from result: %w", err)
	}

	events = make([]XAPIEvent, 0, len(parsed.Events))
	for _, e := range parsed.Events {
		events = append(events, XAPIEvent{
			ID: e.ID, Class: e.Class, Operation: e.Operation, Ref: e.Ref, ObjUUID: e.ObjUUID, Snapshot: e.Snapshot,
		})
	}
	return events, parsed.Token, nil
}
