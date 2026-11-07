package dispatcher_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/dispatcher"
	"github.com/plusclouds/ubuntu-agent/internal/executor"
	"github.com/plusclouds/ubuntu-agent/internal/jobs"
	"github.com/plusclouds/ubuntu-agent/internal/modules/nfsmount"
	"github.com/plusclouds/ubuntu-agent/internal/modules/openvswitch"
	"github.com/plusclouds/ubuntu-agent/internal/modules/system"
	"github.com/plusclouds/ubuntu-agent/internal/modules/xenserver"
	"github.com/plusclouds/ubuntu-agent/internal/protocol"
)

// xe stub for provisioning: logs every call to $XE_LOG.
const provisionXe = `#!/bin/bash
echo "$@" >> "$XE_LOG"
cmd="$1"; shift
case "$cmd" in
  sr-list) for a in "$@"; do case "$a" in uuid=sr-1) echo sr-1; exit 0;; esac; done ;;
  vm-import) echo "new-vm-uuid" ;;
  vm-param-set) ;;
  sr-param-get)
    for a in "$@"; do case "$a" in uuid=iso-sr) echo iso; exit 0;; uuid=nfs-sr) echo nfs; exit 0;; esac; done
    echo "no such sr" >&2; exit 1 ;;
  sr-scan) ;;
  vdi-list) echo "cidata-vdi" ;;
  vbd-list) ;;
  network-list) for a in "$@"; do case "$a" in uuid=net-ok) echo net-ok; exit 0;; esac; done ;;
  vif-create) echo "new-vif" ;;
  *) echo "unhandled xe: $cmd" >&2; exit 1 ;;
esac
`

type provEnv struct {
	d   *dispatcher.Dispatcher
	dir string
	log string
}

func newProvDispatcher(t *testing.T, ops ...string) provEnv {
	t.Helper()
	dir := t.TempDir()
	logf := filepath.Join(dir, "xe.log")
	t.Setenv("XE_LOG", logf)
	xe := writeStub(t, dir, "xe", provisionXe)
	ok := writeStub(t, dir, "true", trueStubScript)
	// genisoimage stub: honours -o <file>, writes the label and file list.
	iso := writeStub(t, dir, "genisoimage", `#!/bin/bash
while [ $# -gt 0 ]; do case "$1" in -o) out="$2"; shift;; -V) label="$2"; shift;; esac; shift; done
echo "ISO label=$label" > "$out"
`)
	logger := zap.NewNop()
	exec := executor.New(logger, false)
	xm := xenserver.New(exec, xe, filepath.Join(dir, "inv"), filepath.Join(dir, "sock"), ok, logger)
	xm.SetISOBinary(iso)
	xm.SetSRMountDir(filepath.Join(dir, "sr-mount"))
	d := dispatcher.New(system.New(nil), fakeServiceManager{}, exec, xm,
		openvswitch.New(exec, ok, ok, xe, logger), nfsmount.New(exec, ok, ok, logger), nil,
		"agent-uuid", ops, nil, logger)
	m := jobs.New(logger, 2, 10)
	t.Cleanup(m.Shutdown)
	d.SetJobs(m)
	return provEnv{d: d, dir: dir, log: logf}
}

func (e provEnv) xeLog() string { b, _ := os.ReadFile(e.log); return string(b) }

func TestVMImportValidatesFirstAndReturnsUUID(t *testing.T) {
	e := newProvDispatcher(t, "xenserver.vm.import")
	ctx := context.Background()
	img := filepath.Join(e.dir, "image.xva")
	os.WriteFile(img, []byte("data"), 0o644)

	for name, params := range map[string]map[string]any{
		"relative path": {"filename": "image.xva"},
		"missing file":  {"filename": filepath.Join(e.dir, "nope.xva")},
		"bad sr":        {"filename": img, "sr_uuid": "x"},
	} {
		p, _ := decode(t, e.d.Dispatch(ctx, buildCommand(t, "xenserver.vm.import", params)))
		if p.Status != protocol.StatusFailed {
			t.Fatalf("%s: want failed, got %s", name, p.Status)
		}
	}
	if strings.Contains(e.xeLog(), "vm-import") {
		t.Fatal("xe vm-import ran for an invalid request")
	}

	p, out := decode(t, e.d.Dispatch(ctx, buildCommand(t, "xenserver.vm.import",
		map[string]any{"filename": img, "sr_uuid": "sr-1", "new_name": "vm-123"})))
	if p.Status != protocol.StatusCompleted || out["success"] != true {
		t.Fatalf("import: %+v %v", p, out)
	}
	data := out["data"].(map[string]any)
	if data["uuid"] != "new-vm-uuid" || data["name_label"] != "vm-123" || data["renamed"] != true {
		t.Fatalf("data: %v", data)
	}
	if !strings.Contains(e.xeLog(), "vm-param-set uuid=new-vm-uuid name-label=vm-123") {
		t.Fatalf("rename not issued: %s", e.xeLog())
	}
}

func TestISOCreateAndRemove(t *testing.T) {
	e := newProvDispatcher(t, "xenserver.iso.create", "xenserver.iso.remove")
	ctx := context.Background()
	os.MkdirAll(filepath.Join(e.dir, "sr-mount", "iso-sr"), 0o755)
	files := map[string]string{"user-data": "#cloud-config\n", "meta-data": "instance-id: x\n"}

	p, out := decode(t, e.d.Dispatch(ctx, buildCommand(t, "xenserver.iso.create",
		map[string]any{"sr_uuid": "iso-sr", "name": "vm1-config", "files": files})))
	if p.Status != protocol.StatusCompleted || out["success"] != true {
		t.Fatalf("create: %+v %v", p, out)
	}
	if out["data"].(map[string]any)["vdi_uuid"] != "cidata-vdi" {
		t.Fatalf("data: %v", out["data"])
	}
	iso := filepath.Join(e.dir, "sr-mount", "iso-sr", "vm1-config.iso")
	if b, err := os.ReadFile(iso); err != nil || !strings.Contains(string(b), "label=cidata") {
		t.Fatalf("iso not built: %v %q", err, b)
	}
	if !strings.Contains(e.xeLog(), "sr-scan uuid=iso-sr") {
		t.Fatalf("no rescan: %s", e.xeLog())
	}
	if p, _ = decode(t, e.d.Dispatch(ctx, buildCommand(t, "xenserver.iso.create",
		map[string]any{"sr_uuid": "iso-sr", "name": "vm1-config", "files": files}))); p.Status != protocol.StatusFailed {
		t.Fatalf("overwrite without flag: %s", p.Status)
	}
	for name, params := range map[string]map[string]any{
		"traversal name": {"sr_uuid": "iso-sr", "name": "../x", "files": files},
		"bad file name":  {"sr_uuid": "iso-sr", "name": "a", "files": map[string]string{"../etc/passwd": "x"}},
		"non-iso sr":     {"sr_uuid": "nfs-sr", "name": "a", "files": files},
	} {
		if p, _ = decode(t, e.d.Dispatch(ctx, buildCommand(t, "xenserver.iso.create", params))); p.Status != protocol.StatusFailed {
			t.Fatalf("%s: want failed, got %s (%s)", name, p.Status, p.Message)
		}
	}

	if p, _ = decode(t, e.d.Dispatch(ctx, buildCommand(t, "xenserver.iso.remove",
		map[string]any{"sr_uuid": "iso-sr", "name": "vm1-config"}))); p.Status != protocol.StatusFailed || !strings.Contains(p.Message, "confirm") {
		t.Fatalf("remove without confirm: %s", p.Status)
	}
	if p, out = decode(t, e.d.Dispatch(ctx, buildCommand(t, "xenserver.iso.remove",
		map[string]any{"sr_uuid": "iso-sr", "name": "vm1-config", "confirm": true}))); p.Status != protocol.StatusCompleted || out["success"] != true {
		t.Fatalf("remove: %+v %v", p, out)
	}
	if _, err := os.Stat(iso); err == nil {
		t.Fatal("iso still present")
	}
}

func TestVIFCreateRejectsUnknownNetwork(t *testing.T) {
	e := newProvDispatcher(t, "xenserver.vif.create")
	// vm resolution uses vm-list; give uuid directly (resolveVM accepts uuid as is).
	p, _ := decode(t, e.d.Dispatch(context.Background(), buildCommand(t, "xenserver.vif.create",
		map[string]any{"uuid": "vm-1", "device": "2", "network_uuid": "net-missing"})))
	if p.Status != protocol.StatusFailed || !strings.Contains(p.Message, "does not exist on this host") {
		t.Fatalf("%+v", p)
	}
	if strings.Contains(e.xeLog(), "vif-create") {
		t.Fatal("vif-create ran for a missing network")
	}
}

func TestAsyncCallback(t *testing.T) {
	e := newProvDispatcher(t, "xenserver.vm.import")
	img := filepath.Join(e.dir, "image.xva")
	os.WriteFile(img, []byte("data"), 0o644)

	var mu sync.Mutex
	var got map[string]any
	var auth string
	done := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		mu.Unlock()
		close(done)
	}))
	defer srv.Close()
	cmd := func(cb map[string]any) protocol.Envelope {
		return buildCommand(t, "xenserver.vm.import", map[string]any{"filename": img, "async": true, "callback": cb})
	}

	// No allowlist configured: refused.
	if p, _ := decode(t, e.d.Dispatch(context.Background(), cmd(map[string]any{"url": srv.URL + "/api/cb/done", "token": "t"}))); p.Status != protocol.StatusRejected {
		t.Fatalf("callbacks must be off by default: %s", p.Status)
	}
	if err := e.d.SetCallbackAllowedURLs([]string{srv.URL + "/api/cb/"}); err != nil {
		t.Fatal(err)
	}
	e.d.SetCallbackHTTPClient(srv.Client())
	// Outside the allowlist and non-https: refused.
	for _, u := range []string{"https://evil.example/done", "http://127.0.0.1/done",
		srv.URL + ".evil.net/done", "https://user:pw@" + strings.TrimPrefix(srv.URL, "https://") + "/done",
		srv.URL + "/api/cb/../admin", srv.URL + "/api/cbx/done", srv.URL + "/", srv.URL} {
		if p, _ := decode(t, e.d.Dispatch(context.Background(), cmd(map[string]any{"url": u}))); p.Status != protocol.StatusRejected {
			t.Fatalf("%s: want rejected, got %s", u, p.Status)
		}
	}
	p, _ := decode(t, e.d.Dispatch(context.Background(), cmd(map[string]any{"url": srv.URL + "/api/cb/done", "token": "secret-token"})))
	if p.Status != protocol.StatusAccepted {
		t.Fatalf("accepted expected: %+v", p)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("callback not delivered")
	}
	mu.Lock()
	defer mu.Unlock()
	if auth != "Bearer secret-token" || got["status"] != "completed" || got["operation"] != "xenserver.vm.import" {
		t.Fatalf("callback: auth=%q body=%v", auth, got)
	}
	res := got["result"].(map[string]any)
	if res["data"].(map[string]any)["uuid"] != "new-vm-uuid" {
		t.Fatalf("result: %v", res)
	}
}

func TestCallbackPrefixValidation(t *testing.T) {
	e := newProvDispatcher(t)
	for _, bad := range []string{"http://x.example/cb/", "https://x.example/cb", "https://u:p@x.example/cb/", "https://x.example/cb/?a=1", "ftp://x/"} {
		if err := e.d.SetCallbackAllowedURLs([]string{bad}); err == nil {
			t.Errorf("prefix %q accepted", bad)
		}
	}
	if err := e.d.SetCallbackAllowedURLs([]string{"https://x.example/cb/"}); err != nil {
		t.Fatal(err)
	}
}

func TestCallbackDoesNotFollowRedirects(t *testing.T) {
	e := newProvDispatcher(t, "xenserver.vm.import")
	img := filepath.Join(e.dir, "image.xva")
	os.WriteFile(img, []byte("data"), 0o644)
	hit := make(chan struct{}, 1)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit <- struct{}{} }))
	defer target.Close()
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/internal", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	if err := e.d.SetCallbackAllowedURLs([]string{redirector.URL + "/cb/"}); err != nil {
		t.Fatal(err)
	}
	e.d.SetCallbackHTTPClient(redirector.Client())
	e.d.SetCallbackRetryDelay(10 * time.Millisecond)
	p, _ := decode(t, e.d.Dispatch(context.Background(), buildCommand(t, "xenserver.vm.import",
		map[string]any{"filename": img, "async": true, "callback": map[string]any{"url": redirector.URL + "/cb/x", "token": "t"}})))
	if p.Status != protocol.StatusAccepted {
		t.Fatalf("%+v", p)
	}
	select {
	case <-hit:
		t.Fatal("callback followed a redirect outside the allowlist")
	case <-time.After(1500 * time.Millisecond):
	}
}
