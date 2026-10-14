package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kryspal/plugin-sdk/sdk"
)

// The OpenSubtitles.com REST API v1 (https://opensubtitles.stoplight.io/docs/opensubtitles-api):
//
//	every request   Api-Key (the application key), User-Agent "<app> v<version>" (required, recognizable), Accept: application/json
//	POST /login     {username, password} → {token, base_url, user:{allowed_downloads, level, vip}}; 401 wrong account; the token lives about 24 hours
//	GET  /subtitles query parameters in lower case; the account is not needed for searching
//	POST /download  Authorization: Bearer <token>; {file_id} → {link, file_name, remaining, reset_time_utc}; 406 when the daily quota is used up
//	                (the body then still carries remaining and reset_time_utc); 429 when requests come too fast (Retry-After)
//	the link        a plain GET without any header, valid a few hours
//
// Terms: a recognizable User-Agent, the application key belongs to the developer (so it is injected, never in the source), no bulk scraping.

const (
	maxResponseBytes = 4 << 20
	requestTimeout   = 30 * time.Second
	// minInterval is the minimum interval between two requests: OpenSubtitles allows a few per second per key; the plugin goes one at a time.
	minInterval       = 400 * time.Millisecond
	defaultRetryAfter = 30 * time.Second
)

var (
	errBadAccount = errors.New("OpenSubtitles rejected the user name or password (sign in with the user name, not the email address)")
	errBadKey     = errors.New("OpenSubtitles rejected the application key")
	errTokenGone  = errors.New("the session token expired")
	// errQuota is a used-up daily download quota; the quotaError carries when it resets.
)

type quotaError struct {
	resetAt   time.Time
	remaining int
}

func (e *quotaError) Error() string { return "OpenSubtitles download quota used up" }

// api is the HTTP client: one request at a time, at least interval apart.
type api struct {
	http     *http.Client
	interval time.Duration
	mu       sync.Mutex
	last     time.Time
}

func newAPI() *api {
	return &api{http: &http.Client{Timeout: requestTimeout}, interval: minInterval}
}

func (a *api) wait(ctx context.Context) error {
	if d := time.Until(a.last.Add(a.interval)); d > 0 {
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
	a.last = time.Now()
	return nil
}

// do sends one request with the common headers and decodes a 200 JSON body into out. The status is returned with the raw body for the caller's
// own handling of 406 and the like; statuses every caller treats the same are mapped here: 429 a rate limit, 5xx and connection errors unavailable.
func (a *api) do(ctx context.Context, method, rawURL, key, token string, body any, out any) (int, []byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.wait(ctx); err != nil {
		return 0, nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, sdk.InvalidArgument("opensubtitles: bad request")
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return 0, nil, sdk.Unavailable("opensubtitles: bad request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Api-Key", key)
	req.Header.Set("User-Agent", userAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, nil, transportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode, nil, sdk.Unavailable("opensubtitles: " + err.Error())
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return resp.StatusCode, raw, sdk.RateLimited(retryAfter(resp), "opensubtitles: rate limit reached")
	case resp.StatusCode >= 500:
		return resp.StatusCode, raw, sdk.Unavailable("opensubtitles: " + resp.Status)
	case resp.StatusCode == http.StatusOK && out != nil:
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, raw, sdk.Unavailable("opensubtitles: bad response")
		}
	}
	return resp.StatusCode, raw, nil
}

// userAgent is the recognizable agent string OpenSubtitles requires: the application name and version.
func userAgent() string { return "Kryspal Media v" + version() }

func retryAfter(resp *http.Response) time.Duration {
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return defaultRetryAfter
}

func transportError(ctx context.Context, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err // url.Error carries the full URL; keep only the cause
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return sdk.Unavailable("opensubtitles: " + err.Error())
}

// fetch downloads the subtitle file from the link the download call returned (a plain GET, no key, no token). It returns nil when the file is
// larger than max or the link no longer works.
func (a *api) fetch(ctx context.Context, rawURL string, max int) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, nil
	}
	u.Scheme = "https"
	a.mu.Lock()
	err = a.wait(ctx)
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, sdk.Unavailable("opensubtitles: bad request")
	}
	req.Header.Set("User-Agent", userAgent())
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 500 {
		return nil, sdk.Unavailable("opensubtitles: " + resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(max)+1))
	if err != nil {
		return nil, sdk.Unavailable("opensubtitles: " + err.Error())
	}
	if len(data) > max {
		return nil, nil
	}
	return data, nil
}

// hostOK reports whether a base_url named by the login is an OpenSubtitles host (the plugin never follows the login to another site).
func hostOK(host string) bool {
	host = strings.ToLower(host)
	return host == "opensubtitles.com" || strings.HasSuffix(host, ".opensubtitles.com")
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }
