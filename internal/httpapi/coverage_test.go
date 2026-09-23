package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestHandlerValidationBranches covers guard branches (unknown ids and
// malformed JSON bodies) across the write handlers that the happy-path
// suite never reaches.
func TestHandlerValidationBranches(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	card := `{"decision":"d","context":"c","priorities":"p","unusual":"u","history":"h","deadline":"dl","style":"s"}`
	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "should return 404 when thread id is unknown",
			method:     http.MethodPut,
			path:       "/api/threads/no-such-thread/card",
			body:       card,
			wantStatus: http.StatusNotFound,
			wantCode:   "not_found",
		},
		{
			name:       "should return 400 when card body is invalid JSON",
			method:     http.MethodPut,
			path:       "/api/threads/" + ts.thread + "/card",
			body:       "{not json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "bad_json",
		},
		{
			name:       "should return 400 when thread_id is missing",
			method:     http.MethodPost,
			path:       "/api/requests",
			body:       `{"thread_id":"","question":"what now?"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "validation_error",
		},
		{
			name:       "should return 400 when messages carry no text",
			method:     http.MethodPost,
			path:       "/api/requests",
			body:       `{"thread_id":"` + ts.thread + `","messages":[{"sender":"them","text":" "}]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "validation_error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			w := doReq(t, ts.srv.Handler(), tc.method, tc.path, tc.body)

			// Assert
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			out := decodeJSON(t, w)
			if out["code"] != tc.wantCode {
				t.Fatalf("code = %v, want %q: %v", out["code"], tc.wantCode, out)
			}
		})
	}
}

// TestCreateRequestUnavailableWhenRunnerNil verifies a read-only server
// (nil runner) rejects live request creation with 503/no_runner.
func TestCreateRequestUnavailableWhenRunnerNil(t *testing.T) {
	// Arrange
	ts := setupTestServer(t, fastScripts())
	ts.srv.runner = nil

	// Act
	w := doReq(t, ts.srv.Handler(), http.MethodPost, "/api/requests",
		`{"thread_id":"`+ts.thread+`","question":"what now?"}`)

	// Assert
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
	out := decodeJSON(t, w)
	if out["code"] != "no_runner" {
		t.Fatalf("code = %v, want no_runner: %v", out["code"], out)
	}
}

// TestRequestScopedHandlerErrorBranches covers 404 for unknown request ids
// and 400 for malformed JSON bodies on the per-request write handlers.
func TestRequestScopedHandlerErrorBranches(t *testing.T) {
	// Arrange
	ts := setupTestServer(t, fastScripts())
	id := runCompletedRequest(t, ts)
	unknown := "/api/requests/no-such-request"
	cases := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "should return 404 when feedback request id is unknown",
			path:       unknown + "/feedback",
			body:       `{"tag":"other","note":"n"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "not_found",
		},
		{
			name:       "should return 400 when feedback body is invalid JSON",
			path:       "/api/requests/" + id + "/feedback",
			body:       "{not json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "bad_json",
		},
		{
			name:       "should return 404 when sent message request id is unknown",
			path:       unknown + "/sent",
			body:       `{"text":"hi"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "not_found",
		},
		{
			name:       "should return 400 when sent body is invalid JSON",
			path:       "/api/requests/" + id + "/sent",
			body:       "{not json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "bad_json",
		},
		{
			name:       "should return 400 when rewrite body is invalid JSON",
			path:       "/api/requests/" + id + "/rewrite",
			body:       "{not json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "bad_json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			w := doReq(t, ts.srv.Handler(), http.MethodPost, tc.path, tc.body)

			// Assert
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			out := decodeJSON(t, w)
			if out["code"] != tc.wantCode {
				t.Fatalf("code = %v, want %q: %v", out["code"], tc.wantCode, out)
			}
		})
	}
}

// TestRawMessageAndSSEEncodeFallbacks verifies the JSON helper fallbacks:
// empty raw messages decode to nil, malformed ones pass through as text,
// and unmarshalable SSE payloads degrade to an empty object.
func TestRawMessageAndSSEEncodeFallbacks(t *testing.T) {
	t.Run("should return nil when raw message is empty", func(t *testing.T) {
		// Act & Assert
		if got := rawMessage(nil); got != nil {
			t.Fatalf("rawMessage(nil) = %#v, want nil", got)
		}
	})
	t.Run("should return the raw text when raw message is not valid JSON", func(t *testing.T) {
		// Act & Assert
		if got := rawMessage(json.RawMessage("{oops")); got != "{oops" {
			t.Fatalf("rawMessage({oops) = %#v, want the raw text {oops", got)
		}
	})
	t.Run("should return empty object when SSE payload cannot be marshalled", func(t *testing.T) {
		// Act & Assert
		if got := sseEncode(make(chan int)); got != "{}" {
			t.Fatalf("sseEncode(chan) = %q, want {}", got)
		}
	})
}

// TestPercentileSortsInputAndClampsIndex verifies percentile sorts its copy
// before indexing and clamps out-of-range percentiles to the ends.
func TestPercentileSortsInputAndClampsIndex(t *testing.T) {
	cases := []struct {
		name string
		in   []int64
		p    float64
		want int64
	}{
		{name: "should return the median when input is unsorted", in: []int64{3, 1, 2}, p: 0.5, want: 2},
		{name: "should return the first element when percentile is negative", in: []int64{3, 1, 2}, p: -1, want: 1},
		{name: "should return the last element when percentile is out of range", in: []int64{1, 2}, p: 2, want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			got := percentile(tc.in, tc.p)

			// Assert
			if got != tc.want {
				t.Fatalf("percentile(%v, %v) = %d, want %d", tc.in, tc.p, got, tc.want)
			}
		})
	}
}
