package xenserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// startFakeEventServer stands up a JSON-RPC server on a temp Unix socket
// that handles login/logout plus event.from, returning a fixed batch of
// events on the first call (empty token) and an empty batch plus a new
// token on subsequent calls — enough to exercise a poller's token handling
// without a real xapi event bus.
func startFakeEventServer(t *testing.T) (socketPath string, cleanup func()) {
	t.Helper()

	dir := t.TempDir()
	socketPath = filepath.Join(dir, "xapi.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listening on unix socket: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding jsonrpc request: %v", err)
			return
		}
		switch req.Method {
		case "session.login_with_password":
			fmt.Fprint(w, `{"result":"OpaqueRef:fake-session","error":null}`)
		case "session.logout":
			fmt.Fprint(w, `{"result":null,"error":null}`)
		case "event.from":
			token, _ := req.Params[2].(string)
			if token == "" {
				fmt.Fprint(w, `{"result":{"token":"tok-1","events":[
					{"id":"1","class":"vm","operation":"mod","ref":"OpaqueRef:vm1","obj_uuid":"vm-uuid-1","snapshot":{"power_state":"Running"}}
				]},"error":null}`)
			} else {
				fmt.Fprintf(w, `{"result":{"token":"tok-2","events":[]},"error":null}`)
			}
		default:
			t.Errorf("unexpected jsonrpc method: %s", req.Method)
		}
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	return socketPath, func() { srv.Close() }
}

func TestEventPoller_PollAdvancesToken(t *testing.T) {
	socketPath, cleanup := startFakeEventServer(t)
	defer cleanup()

	mod := New(nil, "", "", socketPath, "", zap.NewNop())
	poller, err := mod.NewEventPoller(context.Background())
	if err != nil {
		t.Fatalf("NewEventPoller: %v", err)
	}
	defer poller.Close(context.Background())

	events, token, err := poller.Poll(context.Background(), "", 5)
	if err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	if token != "tok-1" {
		t.Errorf("token = %q, want tok-1", token)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Class != "vm" || events[0].ObjUUID != "vm-uuid-1" || events[0].Operation != "mod" {
		t.Errorf("unexpected event: %+v", events[0])
	}

	events, token, err = poller.Poll(context.Background(), token, 5)
	if err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if token != "tok-2" {
		t.Errorf("token = %q, want tok-2", token)
	}
	if len(events) != 0 {
		t.Errorf("got %d events on second poll, want 0", len(events))
	}
}
