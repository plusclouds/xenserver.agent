package xenserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// startFakeXAPIServer stands up a JSON-RPC + /export_raw_vdi HTTP server on a
// temp Unix socket, standing in for a real xapi instance. It returns the
// socket path and a cleanup function.
func startFakeXAPIServer(t *testing.T, exportBody []byte, expectVDI string) (socketPath string, cleanup func()) {
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
		default:
			t.Errorf("unexpected jsonrpc method: %s", req.Method)
		}
	})
	mux.HandleFunc("/export_raw_vdi", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("session_id"); got != "OpaqueRef:fake-session" {
			t.Errorf("session_id: got %q", got)
		}
		if got := r.URL.Query().Get("vdi"); got != expectVDI {
			t.Errorf("vdi: got %q, want %q", got, expectVDI)
		}
		w.Write(exportBody)
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	return socketPath, func() {
		srv.Close()
	}
}

func TestVDIExport_StreamsBodyToFile(t *testing.T) {
	fixture := []byte("fake vhd disk content, could be large in reality")
	socketPath, cleanup := startFakeXAPIServer(t, fixture, "vdi-uuid-123")
	defer cleanup()

	mod := New(nil, "", "", socketPath, "", zap.NewNop())

	destDir := t.TempDir()
	dest := filepath.Join(destDir, "exported.vhd")

	result, err := mod.VDIExport(context.Background(), "vdi-uuid-123", dest, "vhd")
	if err != nil {
		t.Fatalf("VDIExport returned Go error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got failure: %s", result.Message)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading destination file: %v", err)
	}
	if string(got) != string(fixture) {
		t.Errorf("destination file content mismatch:\ngot:  %q\nwant: %q", got, fixture)
	}
}

func TestVDIExport_RequiresVDIUUIDAndFilename(t *testing.T) {
	mod := New(nil, "", "", "/nonexistent.sock", "", zap.NewNop())

	if _, err := mod.VDIExport(context.Background(), "", "/tmp/x", "vhd"); err == nil {
		t.Error("expected error for missing vdi_uuid")
	}
	if _, err := mod.VDIExport(context.Background(), "uuid", "", "vhd"); err == nil {
		t.Error("expected error for missing filename")
	}
}

func TestVDIExport_LoginFailureSurfacesAsActionResult(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "xapi.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":null,"error":{"message":"local root auth not available in this test"}}`)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	mod := New(nil, "", "", socketPath, "", zap.NewNop())

	result, err := mod.VDIExport(context.Background(), "vdi-uuid", filepath.Join(dir, "out.vhd"), "vhd")
	if err != nil {
		t.Fatalf("expected nil Go error (failure reported via ActionResult), got: %v", err)
	}
	if result.Success {
		t.Error("expected Success=false on login failure")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "out.vhd")); statErr == nil {
		t.Error("destination file should not be created on login failure")
	}
}
