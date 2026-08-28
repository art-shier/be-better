package agentprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

func TestHTTPProviderSendsBoundedAuthenticatedSnapshotAndDecodesPlan(t *testing.T) {
	taskID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret-key" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("provider headers = %#v", request.Header)
		}
		var body struct {
			Model   string              `json:"model"`
			Run     model.AgentRun      `json:"run"`
			Context model.AgentSnapshot `json:"context"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "agent-v1" || body.Run.Intent != "安排任务" || len(body.Context.Tasks) != 1 {
			t.Fatalf("provider request = %#v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"summary":"完成分析","steps":[{"title":"检查","detail":"任务","metadata":{}}],"changes":[],"sourceRefs":[]}`))
	}))
	defer server.Close()

	provider, err := NewHTTPProvider(server.URL, "secret-key", "agent-v1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := provider.Analyze(context.Background(), model.AgentSnapshot{
		Run: model.AgentRun{Intent: "安排任务"}, Tasks: []model.Task{{ID: taskID, Title: "Ship", Version: 1}},
	})
	if err != nil || plan.Summary != "完成分析" || len(plan.Steps) != 1 {
		t.Fatalf("Analyze() plan=%#v error=%v", plan, err)
	}
}
