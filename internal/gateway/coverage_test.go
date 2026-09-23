package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLiteLLMChatReturnsTransportErrorWhenServerUnreachable covers Chat's
// transport error branch: a refused connection must map to ErrGateway, not to
// a timeout or a JSON error.
func TestLiteLLMChatReturnsTransportErrorWhenServerUnreachable(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(okHandler(`{"model":"m"}`, nil))
	srv.Close() // nothing is listening on srv.URL any more
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))

	// Act
	_, err := client.Chat(context.Background(), ChatRequest{Model: "m"})

	// Assert
	if err == nil {
		t.Fatal("Chat() error = nil, want transport error")
	}
	if !errors.Is(err, ErrGateway) {
		t.Fatalf("err = %v, want errors.Is ErrGateway", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want a transport error, not ErrTimeout", err)
	}
	if !strings.Contains(err.Error(), "gateway: do") {
		t.Fatalf("err = %v, want it to name the failing stage", err)
	}
}

// TestLiteLLMChatSemaphoreWaitHonoursContext covers Chat's semaphore wait
// branches: while another call holds the only slot, a waiter must surface the
// deadline as ErrTimeout and a cancellation as context.Canceled.
func TestLiteLLMChatSemaphoreWaitHonoursContext(t *testing.T) {
	cases := []struct {
		name   string
		ctx    func() (context.Context, context.CancelFunc)
		wantIs error
	}{
		{
			name: "deadline exceeded",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 30*time.Millisecond)
			},
			wantIs: ErrTimeout,
		},
		{
			name: "context cancelled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantIs: context.Canceled,
		},
	}
	for _, tc := range cases {
		t.Run("should fail the waiter when the cause is "+tc.name, func(t *testing.T) {
			// Arrange: a server that holds the single semaphore slot open.
			var releaseOnce, enteredOnce sync.Once
			entered := make(chan struct{})
			release := make(chan struct{})
			releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseFn()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				enteredOnce.Do(func() { close(entered) })
				<-release
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
			}))
			client := New(&http.Client{}, srv.URL, "k", 1, testLogger(t))
			holderDone := make(chan error, 1)
			go func() {
				_, err := client.Chat(context.Background(), ChatRequest{Model: "m"})
				holderDone <- err
			}()
			<-entered // the in-flight call now owns the semaphore

			// Act: the second call has to wait and must give up via its ctx.
			ctx, cancel := tc.ctx()
			defer cancel()
			_, err := client.Chat(ctx, ChatRequest{Model: "m"})

			// Assert
			if !errors.Is(err, tc.wantIs) {
				t.Fatalf("Chat() err = %v, want errors.Is %v", err, tc.wantIs)
			}
			if !strings.Contains(err.Error(), "acquire semaphore") {
				t.Fatalf("err = %v, want it to name the semaphore stage", err)
			}

			// Release the holder and prove the slot was released again.
			releaseFn()
			if herr := <-holderDone; herr != nil {
				t.Fatalf("holder Chat() err = %v, want nil", herr)
			}
			if _, err := client.Chat(context.Background(), ChatRequest{Model: "m"}); err != nil {
				t.Fatalf("Chat() after waiter = %v, want nil (semaphore released)", err)
			}
		})
	}
}

// TestLiteLLMChatRejectsUnmarshalableSchema covers Chat's request-build
// branch: a malformed json_schema payload must fail before any HTTP call is
// made.
func TestLiteLLMChatRejectsUnmarshalableSchema(t *testing.T) {
	// Arrange
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, `{"model":"m"}`)
	}))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))
	req := ChatRequest{
		Model: "m",
		ResponseFormat: &ResponseFormat{
			Type:       "json_schema",
			SchemaName: "broken",
			Schema:     []byte(`{not json`),
		},
	}

	// Act
	_, err := client.Chat(context.Background(), req)

	// Assert
	if err == nil {
		t.Fatal("Chat() error = nil, want request-build error")
	}
	if !errors.Is(err, ErrGateway) {
		t.Fatalf("err = %v, want errors.Is ErrGateway", err)
	}
	if !strings.Contains(err.Error(), "marshal body") {
		t.Fatalf("err = %v, want it to name the failing stage", err)
	}
	if called {
		t.Fatal("server was called, want the request rejected before any HTTP call")
	}
}

// TestSubstitutionErrorMessage covers SubstitutionError.Error(): callers that
// log the error directly must see which model actually answered.
func TestSubstitutionErrorMessage(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(okHandler(`{"model":"other-vendor-1","choices":[]}`, nil))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))

	// Act
	_, err := client.Chat(context.Background(), ChatRequest{
		Model:                 "m",
		ExpectedModelPrefixes: []string{"gpt-x"},
	})

	// Assert
	if !errors.Is(err, ErrSubstituted) {
		t.Fatalf("err = %v, want errors.Is ErrSubstituted", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "gateway: substituted model: other-vendor-1") {
		t.Fatalf("err = %q, want it to name the returned model", msg)
	}
}
