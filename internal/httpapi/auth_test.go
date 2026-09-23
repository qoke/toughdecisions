package httpapi

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSessionAuthBoundary verifies the exact auth boundary: healthz, the UI
// shell, and POST /api/session stay open while every other route requires
// auth whenever a token is configured.
func TestSessionAuthBoundary(t *testing.T) {
	t.Setenv("COUNCIL_SERVER_TOKEN", "test-token-123")
	ts := setupTestServer(t, fastScripts())
	h := ts.srv.Handler()

	open := []struct{ method, path string }{
		{"GET", "/healthz"},
		{"GET", "/"},
	}
	for _, tc := range open {
		t.Run("open "+tc.method+" "+tc.path, func(t *testing.T) {
			w := doReq(t, h, tc.method, tc.path, "")
			if w.Code == http.StatusUnauthorized {
				t.Fatalf("%s %s: got 401, want open", tc.method, tc.path)
			}
		})
	}

	// Protected route without credentials -> 401 {error, code}.
	w := doReq(t, h, "GET", "/api/threads", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/threads without auth = %d, want 401", w.Code)
	}
	got := decodeJSON(t, w)
	if got["error"] == nil || got["code"] == nil {
		t.Fatalf("401 shape = %v, want {error, code}", got)
	}

	// ?token= query string is NOT accepted.
	w = doReq(t, h, "GET", "/api/threads?token=test-token-123", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/threads?token= = %d, want 401", w.Code)
	}

	// Wrong token via bearer -> 401.
	req := httptestRequest(t, "GET", "/api/threads", "")
	req.Header.Set("Authorization", "Bearer wrong-token")
	w = serveOne(h, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bearer wrong token = %d, want 401", w.Code)
	}

	// Correct bearer -> through.
	req = httptestRequest(t, "GET", "/api/threads", "")
	req.Header.Set("Authorization", "Bearer test-token-123")
	w = serveOne(h, req)
	if w.Code != http.StatusOK {
		t.Fatalf("bearer correct token = %d, want 200: %s", w.Code, w.Body.String())
	}

	// POST /healthz is NOT exempt: only GET /healthz is open, so a POST
	// without credentials still requires auth.
	w = doReq(t, h, "POST", "/healthz", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("POST /healthz without auth = %d, want 401", w.Code)
	}
}

// TestSessionAuthEventSourceAndHealthzPost covers the remaining AC wording:
// an authenticated EventSource-style request (the SSE route with the session
// cookie) reaches the handler, and POST /healthz is not exempt from auth.
func TestSessionAuthEventSourceAndHealthzPost(t *testing.T) {
	t.Setenv("COUNCIL_SERVER_TOKEN", "test-token-123")
	ts := setupTestServer(t, fastScripts())
	h := ts.srv.Handler()

	// Unauthenticated SSE route requires auth.
	id := runCompletedRequest(t, ts)
	w := doReq(t, h, "GET", "/api/requests/"+id+"/events", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET events without auth = %d, want 401", w.Code)
	}

	// Establish the session cookie.
	w = doReq(t, h, "POST", "/api/session", `{"token":"test-token-123"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("session = %d, want 200: %s", w.Code, w.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("no %q cookie set", sessionCookieName)
	}

	// Authenticated EventSource-style request reaches the SSE handler and
	// gets the snapshot frame first.
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, err := http.NewRequest("GET", srv.URL+"/api/requests/"+id+"/events", nil)
	if err != nil {
		t.Fatalf("new SSE request: %v", err)
	}
	req.AddCookie(sessionCookie)
	client := srv.Client()
	client.Timeout = 15 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("authenticated SSE request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated SSE status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("SSE content-type = %q, want text/event-stream", ct)
	}
	if got := readFirstSSEEvent(t, resp); got != "snapshot" {
		t.Fatalf("first SSE event = %q, want snapshot", got)
	}

	// POST /healthz with the session cookie passes the middleware (no 401):
	// the exemption is GET-only, so the mux answers the route decision.
	req = httptestRequest(t, "POST", "/healthz", "")
	req.AddCookie(sessionCookie)
	w = serveOne(h, req)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("POST /healthz with cookie = 401, want the route decision, not auth")
	}
}

// readFirstSSEEvent reads SSE frames until the first event name arrives.
func readFirstSSEEvent(t *testing.T, resp *http.Response) string {
	t.Helper()
	type result struct {
		event string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		var ev string
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "event:") {
				ev = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if line == "" && ev != "" {
				ch <- result{event: ev}
				return
			}
		}
		ch <- result{err: sc.Err()}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read SSE frame: %v", r.err)
		}
		return r.event
	case <-time.After(10 * time.Second):
		t.Fatal("no SSE frame within 10s")
		return ""
	}
}

// TestSessionCookieFlow covers POST /api/session: success sets an HttpOnly
// SameSite=Strict cookie, failure is 401, and the cookie grants access.
func TestSessionCookieFlow(t *testing.T) {
	t.Setenv("COUNCIL_SERVER_TOKEN", "test-token-123")
	ts := setupTestServer(t, fastScripts())
	h := ts.srv.Handler()

	// Failure first.
	w := doReq(t, h, "POST", "/api/session", `{"token":"nope"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("session wrong token = %d, want 401", w.Code)
	}
	got := decodeJSON(t, w)
	if got["error"] == nil || got["code"] == nil {
		t.Fatalf("401 shape = %v, want {error, code}", got)
	}

	// Success sets the cookie.
	w = doReq(t, h, "POST", "/api/session", `{"token":"test-token-123"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("session correct token = %d, want 200: %s", w.Code, w.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("no %q cookie set", sessionCookieName)
	}
	if !sessionCookie.HttpOnly {
		t.Fatalf("cookie HttpOnly = false, want true")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie SameSite = %v, want Strict", sessionCookie.SameSite)
	}

	// Cookie grants access (loopback bind still requires auth when configured).
	req := httptestRequest(t, "GET", "/api/threads", "")
	req.AddCookie(sessionCookie)
	w = serveOne(h, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cookie auth = %d, want 200: %s", w.Code, w.Body.String())
	}

	// Tampered cookie rejected.
	bad := *sessionCookie
	bad.Value = "tampered"
	req = httptestRequest(t, "GET", "/api/threads", "")
	req.AddCookie(&bad)
	w = serveOne(h, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered cookie = %d, want 401", w.Code)
	}
}

// TestNoTokenMeansNoAuthMiddleware verifies auth is installed only when the
// token is non-empty: without a token every route stays open.
func TestNoTokenMeansNoAuthMiddleware(t *testing.T) {
	t.Setenv("COUNCIL_SERVER_TOKEN", "")
	ts := setupTestServer(t, fastScripts())
	h := ts.srv.Handler()

	w := doReq(t, h, "GET", "/api/threads", "")
	if w.Code != http.StatusOK {
		t.Fatalf("no token: GET /api/threads = %d, want 200", w.Code)
	}
	w = doReq(t, h, "POST", "/api/session", `{"token":"anything"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("no token: POST /api/session = %d, want 404", w.Code)
	}
}

// TestWhitespaceTokenMeansNoGate verifies a whitespace-only token counts as
// unset for the middleware (serve refuses non-loopback binds separately).
func TestWhitespaceTokenMeansNoGate(t *testing.T) {
	t.Setenv("COUNCIL_SERVER_TOKEN", "   ")
	ts := setupTestServer(t, fastScripts())
	h := ts.srv.Handler()

	w := doReq(t, h, "GET", "/api/threads", "")
	if w.Code != http.StatusOK {
		t.Fatalf("whitespace token: GET /api/threads = %d, want 200 (treated as unset)", w.Code)
	}
}

// httptestRequest builds a request with an optional JSON body.
func httptestRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	return r
}

// serveOne serves a single request and returns the recorder.
func serveOne(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestUIUsesCookieTransport guards the cookie transport contract in the
// embedded UI: no query-string token anywhere, session via POST /api/session,
// same-origin fetch and cookie-based EventSource.
func TestUIUsesCookieTransport(t *testing.T) {
	if len(uiHTML) == 0 {
		t.Fatal("embedded UI is empty")
	}
	body := string(uiHTML)
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "token=") && !strings.Contains(trimmed, "session-token") {
			t.Fatalf("UI references query-string token: %s", trimmed)
		}
	}
	for _, want := range []string{"/api/session", "credentials", "EventSource(\"/api/requests/\""} {
		if !strings.Contains(body, want) {
			t.Fatalf("UI missing %q", want)
		}
	}
}
