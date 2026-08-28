package agentprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dayorder.local/api/internal/model"
)

const maxProviderResponseBytes = 1 << 20

type HTTPProvider struct {
	endpoint string
	apiKey   string
	model    string
	client   *http.Client
}

func NewHTTPProvider(endpoint, apiKey, providerModel string, timeout time.Duration) (*HTTPProvider, error) {
	endpoint = strings.TrimSpace(endpoint)
	apiKey = strings.TrimSpace(apiKey)
	providerModel = strings.TrimSpace(providerModel)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("agent provider endpoint must be an absolute HTTP or HTTPS URL")
	}
	if apiKey == "" || providerModel == "" {
		return nil, errors.New("agent provider API key and model are required")
	}
	if timeout < time.Second || timeout > 2*time.Minute {
		return nil, errors.New("agent provider timeout must be between 1s and 2m")
	}
	return &HTTPProvider{
		endpoint: endpoint, apiKey: apiKey, model: providerModel,
		client: &http.Client{Timeout: timeout},
	}, nil
}

func (*HTTPProvider) Name() string { return "http" }

func (provider *HTTPProvider) Model() string { return provider.model }

func (provider *HTTPProvider) Analyze(ctx context.Context, snapshot model.AgentSnapshot) (model.AgentPlan, error) {
	payload, err := json.Marshal(struct {
		Model   string              `json:"model"`
		Run     model.AgentRun      `json:"run"`
		Context model.AgentSnapshot `json:"context"`
	}{Model: provider.model, Run: snapshot.Run, Context: snapshot})
	if err != nil {
		return model.AgentPlan{}, fmt.Errorf("encode agent provider request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint, bytes.NewReader(payload))
	if err != nil {
		return model.AgentPlan{}, fmt.Errorf("create agent provider request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+provider.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := provider.client.Do(request)
	if err != nil {
		return model.AgentPlan{}, fmt.Errorf("call agent provider: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return model.AgentPlan{}, fmt.Errorf("agent provider returned status %d", response.StatusCode)
	}
	limited := &io.LimitedReader{R: response.Body, N: maxProviderResponseBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var plan model.AgentPlan
	if err = decoder.Decode(&plan); err != nil {
		return model.AgentPlan{}, fmt.Errorf("decode agent provider response: %w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return model.AgentPlan{}, errors.New("agent provider response contains trailing data")
	}
	if limited.N <= 0 {
		return model.AgentPlan{}, errors.New("agent provider response exceeds size limit")
	}
	return plan, nil
}
