package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
