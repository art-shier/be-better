package agentprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentprotocol"
)

func TestNewDeepSeekRejectsUnsafeEndpoints(t *testing.T) {
	tests := []DeepSeekConfig{
		{Endpoint: "http://example.com/chat", APIKey: "secret"},
		{Endpoint: "https://user@example.com/chat", APIKey: "secret"},
		{Endpoint: "https://example.com/chat#fragment", APIKey: "secret"},
		{Endpoint: "http://127.0.0.1/chat", APIKey: "secret"},
		{Endpoint: "https://example.com/chat", APIKey: ""},
	}
	for _, config := range tests {
		if _, err := NewDeepSeek(config); err == nil {
			t.Fatalf("NewDeepSeek(%#v) accepted unsafe config", config)
		}
	}
	if _, err := NewDeepSeek(DeepSeekConfig{Endpoint: "http://localhost/chat", APIKey: "secret", AllowLoopbackHTTP: true}); err != nil {
		t.Fatalf("explicit localhost development endpoint: %v", err)
	}
}

func TestDeepSeekStreamsSyntheticSSEAndSendsValidatedRequest(t *testing.T) {
	var requestSeen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestSeen.Store(true)
		if request.Header.Get("Authorization") != "Bearer test-key" || request.Header.Get("Accept") != "text/event-stream" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %#v", request.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["model"] != "deepseek-v4-flash" || payload["thinking"].(map[string]any)["type"] != "disabled" {
			t.Errorf("payload = %#v", payload)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte(readDeepSeekFixture(t, "tool-turn.sse")))
	}))
	defer server.Close()
	adapter, err := NewDeepSeek(DeepSeekConfig{Endpoint: server.URL, APIKey: "test-key", AllowLoopbackHTTP: true, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	request := deepSeekCalendarRequest(t)
	events, failures := collectProviderSequence(adapter.Stream(context.Background(), request, TurnOptions{Model: "deepseek-v4-flash", MaxOutputTokens: 256}))
	if !requestSeen.Load() || len(failures) != 0 || len(events) != 2 || events[0].Call == nil || events[0].Call.Name != "dayorder.calendar.read" {
		t.Fatalf("requestSeen=%v events=%#v failures=%#v", requestSeen.Load(), events, failures)
	}
	assertUsage15(t, events[1], agentprotocol.ProviderEventStopReasonToolUse)
}

func TestDeepSeekClassifiesHTTPStatusAndRetryAfterWithoutLeakingBodyOrKey(t *testing.T) {
	future := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Second)
	tests := []struct {
		name       string
		status     int
		retryAfter string
		code       agentprotocol.ErrorCode
		retryable  bool
		wantDelay  time.Duration
	}{
		{name: "rate seconds", status: http.StatusTooManyRequests, retryAfter: "17", code: agentprotocol.ErrorCodeProviderRateLimited, retryable: true, wantDelay: 17 * time.Second},
		{name: "rate date", status: http.StatusTooManyRequests, retryAfter: future.Format(http.TimeFormat), code: agentprotocol.ErrorCodeProviderRateLimited, retryable: true, wantDelay: time.Until(future)},
		{name: "server", status: http.StatusInternalServerError, code: agentprotocol.ErrorCodeProviderUnavailable, retryable: true},
		{name: "unauthorized", status: http.StatusUnauthorized, code: agentprotocol.ErrorCodePermissionDenied, retryable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Retry-After", test.retryAfter)
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte("raw-private-body"))
			}))
			defer server.Close()
			adapter, err := NewDeepSeek(DeepSeekConfig{Endpoint: server.URL, APIKey: "top-secret-key", AllowLoopbackHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			events, failures := collectProviderSequence(adapter.Stream(context.Background(), deepSeekCalendarRequest(t), TurnOptions{Model: "deepseek-v4-flash", MaxOutputTokens: 100}))
			var failure *ProviderError
			if len(events) != 0 || len(failures) != 1 || !errors.As(failures[0], &failure) || failure.Code != test.code || failure.Status != test.status || failure.Retryable != test.retryable {
				t.Fatalf("events=%#v failures=%#v", events, failures)
			}
			if strings.Contains(failure.Error(), "top-secret-key") || strings.Contains(failure.Error(), "raw-private-body") {
				t.Fatalf("unsafe error = %q", failure.Error())
			}
			if test.retryAfter != "" && absDuration(failure.RetryAfter-test.wantDelay) > 2*time.Second {
				t.Fatalf("RetryAfter = %v, want approximately %v", failure.RetryAfter, test.wantDelay)
			}
		})
	}
}

func TestDeepSeekDoesNotFollowRedirect(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	adapter, err := NewDeepSeek(DeepSeekConfig{Endpoint: redirect.URL, APIKey: "key", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	_, failures := collectProviderSequence(adapter.Stream(context.Background(), deepSeekCalendarRequest(t), TurnOptions{Model: "deepseek-v4-flash", MaxOutputTokens: 100}))
	if len(failures) != 1 || targetCalls.Load() != 0 {
		t.Fatalf("failures=%#v targetCalls=%d", failures, targetCalls.Load())
	}
}

func TestDeepSeekCancellationClosesStreamingRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	adapter, err := NewDeepSeek(DeepSeekConfig{Endpoint: server.URL, APIKey: "key", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []error, 1)
	go func() {
		_, failures := collectProviderSequence(adapter.Stream(ctx, deepSeekCalendarRequest(t), TurnOptions{Model: "deepseek-v4-flash", MaxOutputTokens: 100}))
		done <- failures
	}()
	<-started
	cancel()
	select {
	case failures := <-done:
		var failure *ProviderError
		if len(failures) != 1 || !errors.As(failures[0], &failure) || failure.Code != agentprotocol.ErrorCodeCancelled {
			t.Fatalf("failures = %#v", failures)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
}

func TestDeepSeekPreservesKnownUsageFromInvalidTurn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2,\"total_tokens\":9}}\n\n"))
	}))
	defer server.Close()
	adapter, err := NewDeepSeek(DeepSeekConfig{Endpoint: server.URL, APIKey: "key", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	events, failures := collectProviderSequence(adapter.Stream(context.Background(), deepSeekCalendarRequest(t), TurnOptions{Model: "deepseek-v4-flash", MaxOutputTokens: 100}))
	var failure *ProviderError
	if len(events) != 0 || len(failures) != 1 || !errors.As(failures[0], &failure) || !reflect.DeepEqual(failure.KnownUsage, &agentprotocol.Usage{InputTokens: 7, OutputTokens: 2, TotalTokens: 9}) {
		t.Fatalf("events=%#v failures=%#v", events, failures)
	}
}

func TestDeepSeekValidatesParsedArgumentsAgainstOriginalToolSpecBeforeYieldingCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte(readDeepSeekFixture(t, "tool-turn.sse")))
	}))
	defer server.Close()
	adapter, err := NewDeepSeek(DeepSeekConfig{Endpoint: server.URL, APIKey: "key", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	request := deepSeekCalendarRequest(t)
	request.Tools[0].InputSchema["required"] = []any{"start", "end", "nonce"}
	request.Tools[0].InputSchema["properties"].(map[string]any)["nonce"] = map[string]any{"type": "string"}
	events, failures := collectProviderSequence(adapter.Stream(context.Background(), request, TurnOptions{Model: "deepseek-v4-flash", MaxOutputTokens: 100}))
	var failure *ProviderError
	if len(events) != 0 || len(failures) != 1 || !errors.As(failures[0], &failure) || !reflect.DeepEqual(failure.KnownUsage, &agentprotocol.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}) {
		t.Fatalf("events=%#v failures=%#v", events, failures)
	}
}

func TestDeepSeekMalformedFrameFailsWithoutCompleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[\n\n"))
	}))
	defer server.Close()
	adapter, err := NewDeepSeek(DeepSeekConfig{Endpoint: server.URL, APIKey: "key", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	events, failures := collectProviderSequence(adapter.Stream(context.Background(), deepSeekCalendarRequest(t), TurnOptions{Model: "deepseek-v4-flash", MaxOutputTokens: 100}))
	if len(events) != 0 || len(failures) != 1 {
		t.Fatalf("events=%#v failures=%#v", events, failures)
	}
}

func TestDeepSeekPropagatesBodyTransportFailuresWithKnownUsage(t *testing.T) {
	validUsage := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2,\"total_tokens\":9}}\n\n"
	tests := []struct {
		name       string
		body       string
		knownUsage *agentprotocol.Usage
	}{
		{name: "before publication"},
		{name: "after valid usage", body: validUsage, knownUsage: &agentprotocol.Usage{InputTokens: 7, OutputTokens: 2, TotalTokens: 9}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &sourceErrorBody{sourceErrorReader: sourceErrorReader{data: []byte(test.body), failure: io.ErrUnexpectedEOF}}}, nil
			})}
			adapter, err := NewDeepSeek(DeepSeekConfig{Endpoint: "https://example.invalid/chat", APIKey: "key", Client: client})
			if err != nil {
				t.Fatal(err)
			}
			events, failures := collectProviderSequence(adapter.Stream(context.Background(), deepSeekCalendarRequest(t), TurnOptions{Model: "deepseek-v4-flash", MaxOutputTokens: 100}))
			var failure *ProviderError
			if len(events) != 0 || len(failures) != 1 || !errors.As(failures[0], &failure) || failure.Code != agentprotocol.ErrorCodeProviderUnavailable || !failure.Retryable || !reflect.DeepEqual(failure.KnownUsage, test.knownUsage) {
				t.Fatalf("events=%#v failures=%#v", events, failures)
			}
		})
	}
}

func TestParseRetryAfterSaturatesUnrepresentableSeconds(t *testing.T) {
	const maximum = time.Duration(9223372036854775807)
	tests := map[string]struct {
		value string
		want  time.Duration
	}{
		"ordinary":                {value: "17", want: 17 * time.Second},
		"multiplication boundary": {value: "9223372036", want: 9223372036 * time.Second},
		"first unrepresentable":   {value: "9223372037", want: maximum},
		"int64 seconds overflow":  {value: "9223372036854775807", want: maximum},
		"beyond int64":            {value: "9223372036854775808", want: maximum},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := parseRetryAfter(test.value, time.Unix(0, 0)); got != test.want {
				t.Fatalf("parseRetryAfter(%q)=%v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestIdleControllerResetsOnProgressThenCancelsAndClosesBody(t *testing.T) {
	cancelled := make(chan struct{})
	var cancelOnce atomic.Bool
	controller := newIdleControllerWithTimeout(func() {
		if cancelOnce.CompareAndSwap(false, true) {
			close(cancelled)
		}
	}, 100*time.Millisecond)
	defer controller.stop()
	body := &countingCloser{}
	controller.setBody(body)
	for range 3 {
		time.Sleep(20 * time.Millisecond)
		controller.reset()
	}
	select {
	case <-cancelled:
		t.Fatal("controller expired despite read progress")
	default:
	}
	select {
	case <-cancelled:
		if !controller.expired() || body.calls.Load() != 1 {
			t.Fatalf("expired=%v close calls=%d", controller.expired(), body.calls.Load())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("controller did not expire after idle period")
	}
}

func deepSeekCalendarRequest(t *testing.T) agentprotocol.ModelTurnRequest {
	t.Helper()
	calendar, err := agentassets.CalendarReadSpec()
	if err != nil {
		t.Fatal(err)
	}
	return agentprotocol.ModelTurnRequest{ProtocolVersion: "2.0", RunID: "run-deepseek", TurnID: "turn-deepseek", ModelProfile: "server/default", Messages: []agentprotocol.Message{textMessage(agentprotocol.MessageRoleUser, "calendar")}, Tools: []agentprotocol.ToolSpec{calendar}}
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

type countingCloser struct{ calls atomic.Int32 }

func (closer *countingCloser) Close() error {
	closer.calls.Add(1)
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type sourceErrorBody struct{ sourceErrorReader }

func (*sourceErrorBody) Close() error { return nil }
