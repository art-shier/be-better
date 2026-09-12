package agentprovider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"iter"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"dayorder.local/api/internal/agentprotocol"
)

type DeepSeek struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

func NewDeepSeek(config DeepSeekConfig) (*DeepSeek, error) {
	endpoint := strings.TrimSpace(config.Endpoint)
	apiKey := strings.TrimSpace(config.APIKey)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || apiKey == "" {
		return nil, errors.New("invalid DeepSeek configuration")
	}
	if parsed.Scheme != "https" {
		if parsed.Scheme != "http" || !config.AllowLoopbackHTTP || !loopbackHost(parsed.Hostname()) {
			return nil, errors.New("invalid DeepSeek endpoint")
		}
	}
	client := config.Client
	if client == nil {
		client = &http.Client{}
	} else {
		clone := *client
		client = &clone
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &DeepSeek{endpoint: endpoint, apiKey: apiKey, client: client}, nil
}

func loopbackHost(host string) bool {
	address := net.ParseIP(host)
	if address != nil {
		return address.IsLoopback()
	}
	addresses, err := net.LookupIP(host)
	if err != nil || len(addresses) == 0 {
		return false
	}
	for _, candidate := range addresses {
		if !candidate.IsLoopback() {
			return false
		}
	}
	return true
}

func (provider *DeepSeek) Stream(ctx context.Context, turn agentprotocol.ModelTurnRequest, options TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		payload, err := EncodeDeepSeekRequest(turn, options)
		if err != nil {
			yield(agentprotocol.ProviderEvent{}, err)
			return
		}
		requestCtx, cancel := context.WithCancel(ctx)
		idle := newIdleController(cancel)
		defer func() {
			idle.stop()
			cancel()
		}()
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, provider.endpoint, bytes.NewReader(payload))
		if err != nil {
			yield(agentprotocol.ProviderEvent{}, protocolError())
			return
		}
		request.Header.Set("Authorization", "Bearer "+provider.apiKey)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")

		response, err := provider.client.Do(request)
		if err != nil {
			yield(agentprotocol.ProviderEvent{}, classifyTransportFailure(ctx, idle, err))
			return
		}
		idle.setBody(response.Body)
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode > 299 {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			yield(agentprotocol.ProviderEvent{}, classifyDeepSeekStatus(response))
			return
		}
		if mediaType := strings.ToLower(response.Header.Get("Content-Type")); !strings.HasPrefix(mediaType, "text/event-stream") {
			yield(agentprotocol.ProviderEvent{}, protocolError())
			return
		}

		specs := make(map[string]agentprotocol.ToolSpec, len(turn.Tools))
		for _, spec := range turn.Tools {
			specs[spec.ID] = spec
		}
		var pendingCall *agentprotocol.ToolCall
		for event, parseErr := range ParseDeepSeekStream(requestCtx, idleReader{source: response.Body, idle: idle}) {
			if parseErr != nil {
				yield(agentprotocol.ProviderEvent{}, normalizeStreamFailure(ctx, idle, parseErr))
				return
			}
			if event.Type == agentprotocol.ProviderEventTypeToolCall {
				if event.Call == nil || pendingCall != nil {
					yield(agentprotocol.ProviderEvent{}, protocolError())
					return
				}
				pendingCall = event.Call
				continue
			}
			if event.Type == agentprotocol.ProviderEventTypeCompleted && pendingCall != nil {
				spec, exists := specs[pendingCall.Name]
				if !exists || !validToolInput(spec.InputSchema, pendingCall.Input) {
					failure := &ProviderError{Code: agentprotocol.ErrorCodeProtocolIncompatible, Retryable: false}
					if event.Usage != nil {
						known := *event.Usage
						failure.KnownUsage = &known
					}
					yield(agentprotocol.ProviderEvent{}, failure)
					return
				}
				if !yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeToolCall, Call: pendingCall}, nil) {
					return
				}
				pendingCall = nil
			}
			if !yield(event, nil) {
				return
			}
		}
	}
}

type idleController struct {
	mu         sync.Mutex
	timer      *time.Timer
	timeout    time.Duration
	generation uint64
	cancel     context.CancelFunc
	body       io.Closer
	timedOut   bool
}

func newIdleController(cancel context.CancelFunc) *idleController {
	return newIdleControllerWithTimeout(cancel, providerIdleTimeout)
}

func newIdleControllerWithTimeout(cancel context.CancelFunc, timeout time.Duration) *idleController {
	controller := &idleController{cancel: cancel, timeout: timeout}
	controller.scheduleLocked()
	return controller
}

func (controller *idleController) scheduleLocked() {
	controller.generation++
	generation := controller.generation
	controller.timer = time.AfterFunc(controller.timeout, func() { controller.expire(generation) })
}

func (controller *idleController) expire(generation uint64) {
	controller.mu.Lock()
	if controller.timedOut || generation != controller.generation {
		controller.mu.Unlock()
		return
	}
	controller.timedOut = true
	body := controller.body
	controller.mu.Unlock()
	controller.cancel()
	if body != nil {
		_ = body.Close()
	}
}

func (controller *idleController) reset() {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.timedOut {
		controller.timer.Stop()
		controller.scheduleLocked()
	}
}

func (controller *idleController) setBody(body io.Closer) {
	controller.mu.Lock()
	controller.body = body
	timedOut := controller.timedOut
	controller.mu.Unlock()
	if timedOut {
		_ = body.Close()
	}
}

func (controller *idleController) stop() {
	controller.mu.Lock()
	controller.generation++
	controller.timer.Stop()
	controller.mu.Unlock()
}

func (controller *idleController) expired() bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.timedOut
}

type idleReader struct {
	source io.Reader
	idle   *idleController
}

func (reader idleReader) Read(buffer []byte) (int, error) {
	count, err := reader.source.Read(buffer)
	if count > 0 {
		reader.idle.reset()
	}
	if reader.idle.expired() {
		return count, &ProviderError{Code: agentprotocol.ErrorCodeTimeout, Retryable: true}
	}
	return count, err
}

func classifyTransportFailure(parent context.Context, idle *idleController, err error) error {
	if idle.expired() {
		return &ProviderError{Code: agentprotocol.ErrorCodeTimeout, Retryable: true}
	}
	if parent.Err() != nil {
		return &ProviderError{Code: agentprotocol.ErrorCodeCancelled, Retryable: false}
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return &ProviderError{Code: agentprotocol.ErrorCodeTimeout, Retryable: true}
	}
	return &ProviderError{Code: agentprotocol.ErrorCodeProviderUnavailable, Retryable: true}
}

func normalizeStreamFailure(parent context.Context, idle *idleController, err error) error {
	if idle.expired() {
		return providerFailureWithKnownUsage(agentprotocol.ErrorCodeTimeout, true, err)
	}
	if parent.Err() != nil {
		return providerFailureWithKnownUsage(agentprotocol.ErrorCodeCancelled, false, err)
	}
	return err
}

func providerFailureWithKnownUsage(code agentprotocol.ErrorCode, retryable bool, source error) error {
	failure := &ProviderError{Code: code, Retryable: retryable}
	var previous *ProviderError
	if errors.As(source, &previous) && previous.KnownUsage != nil {
		known := *previous.KnownUsage
		failure.KnownUsage = &known
	}
	return failure
}

func classifyDeepSeekStatus(response *http.Response) error {
	failure := &ProviderError{Status: response.StatusCode}
	switch response.StatusCode {
	case http.StatusTooManyRequests:
		failure.Code = agentprotocol.ErrorCodeProviderRateLimited
		failure.Retryable = true
		failure.RetryAfter = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
	case http.StatusUnauthorized, http.StatusForbidden:
		failure.Code = agentprotocol.ErrorCodePermissionDenied
	case http.StatusRequestTimeout:
		failure.Code = agentprotocol.ErrorCodeTimeout
		failure.Retryable = true
	default:
		failure.Code = agentprotocol.ErrorCodeProviderUnavailable
		failure.Retryable = response.StatusCode >= 500
	}
	return failure
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if decimalSeconds(value) {
		const maximum = time.Duration(1<<63 - 1)
		const maximumWholeSeconds = "9223372036"
		normalized := strings.TrimLeft(value, "0")
		if normalized == "" {
			return 0
		}
		if len(normalized) > len(maximumWholeSeconds) || len(normalized) == len(maximumWholeSeconds) && normalized > maximumWholeSeconds {
			return maximum
		}
		seconds, _ := strconv.ParseInt(normalized, 10, 64)
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func decimalSeconds(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

var _ Adapter = (*DeepSeek)(nil)
