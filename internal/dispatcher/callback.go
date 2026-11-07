package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/jobs"
)

// callbackParam asks the agent to POST an async job's final state to a URL.
type callbackParam struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// SetCallbackAllowedURLs sets the URL prefixes callbacks may target. With none
// configured, callbacks are refused: an agent must not be an open relay that
// POSTs to arbitrary addresses on behalf of a caller. Each prefix must be an
// https URL whose path ends in "/" (for example https://host/api/callbacks/);
// invalid prefixes are rejected and reported by the returned error.
func (d *Dispatcher) SetCallbackAllowedURLs(prefixes []string) error {
	d.callbackPrefixes = nil
	for _, p := range prefixes {
		u, err := url.Parse(p)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("agent.callback_allowed_urls entry %q must be an https URL without credentials, query or fragment", p)
		}
		if !strings.HasSuffix(u.Path, "/") {
			return fmt.Errorf("agent.callback_allowed_urls entry %q must end with '/'", p)
		}
		d.callbackPrefixes = append(d.callbackPrefixes, p)
	}
	return nil
}

// SetCallbackRetryDelay sets the first retry delay (tests).
func (d *Dispatcher) SetCallbackRetryDelay(v time.Duration) { d.callbackRetryDelay = v }

// SetCallbackHTTPClient overrides the HTTP client used for callbacks (tests).
func (d *Dispatcher) SetCallbackHTTPClient(c *http.Client) { d.callbackClient = c }

// checkCallback validates a requested callback against the allowlist.
func (d *Dispatcher) checkCallback(cb *callbackParam) error {
	if cb == nil {
		return nil
	}
	if len(d.callbackPrefixes) == 0 {
		return fmt.Errorf("callbacks are not enabled on this agent (agent.callback_allowed_urls is empty)")
	}
	u, err := url.Parse(cb.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return fmt.Errorf("callback url must be an https URL without credentials or fragment")
	}
	// Validate exactly what will be sent: no percent-encoded path characters,
	// no dot segments or doubled slashes, so every parser reads one path.
	reqPath := u.Path
	cleaned := path.Clean(reqPath)
	if strings.HasSuffix(reqPath, "/") && cleaned != "/" {
		cleaned += "/"
	}
	if reqPath == "" || u.RawPath != "" || cleaned != reqPath {
		return fmt.Errorf("callback url path must be plain: no encoded characters, '.'/'..' segments or '//'")
	}
	if !strings.HasSuffix(reqPath, "/") {
		reqPath += "/"
	}
	for _, pre := range d.callbackPrefixes {
		pu, perr := url.Parse(pre)
		if perr != nil {
			continue
		}
		// Exact host and port (so "good.example.com.evil.net" and
		// "good.example.com@evil.net" never match), then a path-segment prefix
		// on the cleaned path (so "/api/callbacks/../admin" cannot escape).
		if strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), strings.TrimSuffix(pu.Hostname(), ".")) &&
			effectivePort(u) == effectivePort(pu) &&
			strings.HasPrefix(reqPath, pu.Path) {
			// Send the rebuilt URL, not the caller's string.
			cb.URL = (&url.URL{Scheme: "https", Host: u.Host, Path: u.Path, RawQuery: u.RawQuery}).String()
			return nil
		}
	}
	return fmt.Errorf("callback url is not under any agent.callback_allowed_urls prefix")
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	return "443"
}

// sendCallback POSTs the finished job to the callback URL, retrying a few
// times with backoff. The token, if any, is sent as a bearer token and never
// logged.
func (d *Dispatcher) sendCallback(cb callbackParam, j jobs.Job) {
	body, err := json.Marshal(map[string]any{
		"agent_uuid": d.agentUUID,
		"job_id":     j.ID,
		"operation":  j.Operation,
		"status":     j.Status,
		"error":      j.Error,
		"elapsed_s":  j.ElapsedS,
		"result":     j.Result,
	})
	if err != nil {
		d.logger.Warn("callback: cannot encode job", zap.String("job_id", j.ID), zap.Error(err))
		return
	}
	client := d.callbackClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	// Never follow redirects: an allowed host must not be able to bounce the
	// agent (with its bearer token) to an address outside the allowlist.
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client = &c
	delay := d.callbackRetryDelay
	if delay == 0 {
		delay = 2 * time.Second
	}
	for attempt := 1; attempt <= 4; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cb.URL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cb.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cb.Token)
		}
		resp, err := client.Do(req)
		cancel()
		if err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if code >= 200 && code < 300 {
				d.logger.Info("callback delivered", zap.String("job_id", j.ID), zap.Int("attempt", attempt))
				return
			}
			err = fmt.Errorf("HTTP %d", code)
		}
		d.logger.Warn("callback failed", zap.String("job_id", j.ID), zap.Int("attempt", attempt), zap.Error(err))
		if attempt < 4 {
			time.Sleep(delay)
			delay *= 3
		}
	}
	d.logger.Error("callback gave up; poll agent.job.status instead", zap.String("job_id", j.ID))
}
