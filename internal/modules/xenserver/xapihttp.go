package xenserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/plusclouds/ubuntu-agent/internal/jobs"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// jsonRPCRequest is a minimal XenAPI JSON-RPC request envelope.
type jsonRPCRequest struct {
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
	JSONRPC string        `json:"jsonrpc"`
}

// jsonRPCResponse is a minimal XenAPI JSON-RPC response envelope.
type jsonRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// newXAPIHTTPClient returns an http.Client whose transport dials the local
// xapi Unix domain socket instead of TCP — every request, regardless of URL,
// is actually carried over that socket.
func newXAPIHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

// callXAPIMethod issues an arbitrary XenAPI JSON-RPC call over client and
// returns the raw "result" field. Every XenAPI method except
// session.login_with_password takes a session ref as its first param.
func callXAPIMethod(ctx context.Context, client *http.Client, method string, params []interface{}) (json.RawMessage, error) {
	reqBody, err := json.Marshal(jsonRPCRequest{
		Method:  method,
		Params:  params,
		ID:      0,
		JSONRPC: "2.0",
	})
	if err != nil {
		return nil, fmt.Errorf("building %s request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/jsonrpc", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("building %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", method, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading %s response: %w", method, err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("parsing %s response: %w", method, err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("%s failed: %s", method, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

// xapiLogin obtains a session ref via session.login_with_password. Empty
// credentials over the local Unix socket are implicitly trusted as local
// root — the same mechanism the xe binary itself relies on, so no password
// needs to be stored or managed by the agent.
func xapiLogin(ctx context.Context, client *http.Client) (string, error) {
	result, err := callXAPIMethod(ctx, client, "session.login_with_password", []interface{}{"", ""})
	if err != nil {
		return "", fmt.Errorf("xapi login: %w", err)
	}

	var sessionRef string
	if err := json.Unmarshal(result, &sessionRef); err != nil {
		return "", fmt.Errorf("parsing session ref: %w", err)
	}
	if sessionRef == "" {
		return "", fmt.Errorf("xapi login returned an empty session ref")
	}
	return sessionRef, nil
}

// xapiLogout releases the session. Best-effort — the session expires on its
// own, so a logout failure never fails the calling operation.
func xapiLogout(ctx context.Context, client *http.Client, sessionRef string) {
	_, _ = callXAPIMethod(ctx, client, "session.logout", []interface{}{sessionRef})
}

// VDIExport streams a VDI's raw disk content to destFilename via xapi's HTTP
// export handler over the local Unix socket. xe has no subprocess-level
// equivalent — this is the only way to get a single disk's raw bytes out.
// format is typically "vhd" or "raw".
//
// Flagged for validation against real infrastructure: the general shape
// (local-socket ⇒ trusted, no password needed) mirrors how xe itself
// authenticates, but the exact JSON-RPC method/param names and socket path
// should be confirmed against the target XenServer/XCP-ng version.
func (m *Module) VDIExport(ctx context.Context, vdiUUID, destFilename, format string) (*ActionResult, error) {
	if vdiUUID == "" || destFilename == "" {
		return nil, fmt.Errorf("vdi_uuid and filename are required")
	}
	if err := checkExportTarget(destFilename); err != nil {
		return nil, err
	}
	if format == "" {
		format = "vhd"
	}

	client := newXAPIHTTPClient(m.xapiSocketPath)

	sessionRef, err := xapiLogin(ctx, client)
	if err != nil {
		return &ActionResult{Operation: "vdi-export", Target: vdiUUID, Success: false, Message: err.Error()}, nil
	}
	defer xapiLogout(context.Background(), client, sessionRef)

	exportURL := fmt.Sprintf("http://localhost/export_raw_vdi?session_id=%s&vdi=%s&format=%s", sessionRef, vdiUUID, format)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, exportURL, nil)
	if err != nil {
		return &ActionResult{Operation: "vdi-export", Target: vdiUUID, Success: false, Message: err.Error()}, nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return &ActionResult{Operation: "vdi-export", Target: vdiUUID, Success: false,
			Message: fmt.Sprintf("export request failed: %s", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &ActionResult{Operation: "vdi-export", Target: vdiUUID, Success: false,
			Message: fmt.Sprintf("export request returned status %d: %s", resp.StatusCode, htmlToText(string(body)))}, nil
	}

	f, err := os.OpenFile(partialPath(destFilename), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return &ActionResult{Operation: "vdi-export", Target: vdiUUID, Success: false,
			Message: fmt.Sprintf("creating destination file: %s", err)}, nil
	}
	var estimate int64
	if jobs.InJob(ctx) { // the estimate is only used for background-job progress
		estimate = m.vdiChainBytes(ctx, vdiUUID)
	}
	stop := trackFile(ctx, partialPath(destFilename), estimate)
	written, err := io.Copy(f, resp.Body)
	stop()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		r := &ActionResult{Operation: "vdi-export", Target: vdiUUID, Success: false,
			Message: fmt.Sprintf("writing export data: %s", err)}
		r.Message += removePartial(partialPath(destFilename))
		return r, nil
	}
	if ferr := finalizeExport(destFilename); ferr != nil {
		return &ActionResult{Operation: "vdi-export", Target: vdiUUID, Success: false,
			Message: fmt.Sprintf("export finished but could not be moved to %s: %v", destFilename, ferr)}, nil
	}

	return &ActionResult{Operation: "vdi-export", Target: vdiUUID, Success: true,
		Message: "VDI exported successfully.",
		Output:  fmt.Sprintf("%s (%d bytes)", destFilename, written)}, nil
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// htmlToText flattens an XAPI HTML error page into one readable line.
func htmlToText(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&quot;", `"`, "&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
