// Package agenttool defines the background runtime's local Tool bindings and
// capability intersection. It deliberately contains no transport or domain
// application dependencies.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"dayorder.local/api/internal/agentprotocol"
)

// Context identifies the Run and Tool Call passed to a Binding invocation.
type Context struct {
	RunID  string
	CallID string
}

// Binding provides one trusted in-process background-runtime implementation
// for a ToolSpec. Invoke must observe ctx and return promptly after ctx.Done;
// code that requires forced termination belongs behind a process boundary.
type Binding interface {
	Spec() agentprotocol.ToolSpec
	Invoke(context.Context, map[string]any, Context) (agentprotocol.ToolResult, error)
}

// Policy is system policy metadata used to filter requested Tools. Deny takes
// precedence over Allow; ApprovalFor is retained for approval resolution.
type Policy struct {
	Allow       []string
	Deny        []string
	ApprovalFor []agentprotocol.SideEffect
}

// Registry holds local background Tool bindings. Its zero value is ready for
// use.
type Registry struct {
	mu       sync.RWMutex
	bindings map[string]storedBinding
}

type storedBinding struct {
	spec   agentprotocol.ToolSpec
	invoke func(context.Context, map[string]any, Context) (agentprotocol.ToolResult, error)
}

type resolvedBinding struct {
	spec   agentprotocol.ToolSpec
	invoke func(context.Context, map[string]any, Context) (agentprotocol.ToolResult, error)
}

func (b resolvedBinding) Spec() agentprotocol.ToolSpec {
	return mustCopySpec(b.spec)
}

func (b resolvedBinding) Invoke(ctx context.Context, input map[string]any, toolContext Context) (agentprotocol.ToolResult, error) {
	return b.invoke(ctx, input, toolContext)
}

// Register validates and stores an independent copy of binding's ToolSpec.
// Duplicate Tool IDs are rejected.
func (r *Registry) Register(binding Binding) error {
	if binding == nil {
		return fmt.Errorf("tool binding is nil")
	}

	spec := binding.Spec()
	if err := agentprotocol.Validate(agentprotocol.DefinitionToolSpec, spec); err != nil {
		return err
	}
	copiedSpec, err := copySpec(spec)
	if err != nil {
		return fmt.Errorf("copy tool spec: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bindings == nil {
		r.bindings = make(map[string]storedBinding)
	}
	if _, exists := r.bindings[copiedSpec.ID]; exists {
		return fmt.Errorf("duplicate tool ID: %s", copiedSpec.ID)
	}
	r.bindings[copiedSpec.ID] = storedBinding{spec: copiedSpec, invoke: binding.Invoke}
	return nil
}

// Resolve returns an independent Binding view for id.
func (r *Registry) Resolve(id string) (Binding, bool) {
	r.mu.RLock()
	binding, ok := r.bindings[id]
	if ok {
		binding.spec = mustCopySpec(binding.spec)
	}
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return resolvedBinding{spec: binding.spec, invoke: binding.invoke}, true
}

// Specs returns independent ToolSpec copies sorted by Tool ID.
func (r *Registry) Specs() []agentprotocol.ToolSpec {
	r.mu.RLock()
	specs := make([]agentprotocol.ToolSpec, 0, len(r.bindings))
	for _, binding := range r.bindings {
		specs = append(specs, mustCopySpec(binding.spec))
	}
	r.mu.RUnlock()

	sort.Slice(specs, func(i, j int) bool {
		return specs[i].ID < specs[j].ID
	})
	return specs
}

// EffectiveToolIDs returns the Tool-ID-sorted intersection of requested Tools,
// the frozen Run grant, registered bindings, Run Scope, execution mode, and
// system policy.
func EffectiveToolIDs(requested []string, registry *Registry, snapshot agentprotocol.CapabilitySnapshot, policy Policy) []string {
	if registry == nil {
		return nil
	}

	allowed := make(map[string]struct{}, len(policy.Allow))
	for _, id := range policy.Allow {
		allowed[id] = struct{}{}
	}
	denied := make(map[string]struct{}, len(policy.Deny))
	for _, id := range policy.Deny {
		denied[id] = struct{}{}
	}
	granted := make(map[string]struct{}, len(snapshot.ToolIds))
	for _, id := range snapshot.ToolIds {
		granted[id] = struct{}{}
	}
	domains := make(map[string]struct{}, len(snapshot.Scope.Domains))
	for _, domain := range snapshot.Scope.Domains {
		domains[domain] = struct{}{}
	}

	effective := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, id := range requested {
		if _, alreadySeen := seen[id]; alreadySeen {
			continue
		}
		seen[id] = struct{}{}
		if _, grantedByRun := granted[id]; !grantedByRun {
			continue
		}

		if _, deniedByPolicy := denied[id]; deniedByPolicy {
			continue
		}
		if _, explicitlyAllowed := allowed[id]; !explicitlyAllowed {
			if _, wildcardAllowed := allowed["*"]; !wildcardAllowed {
				continue
			}
		}

		binding, found := registry.Resolve(id)
		if !found {
			continue
		}
		spec := binding.Spec()
		if !supportsMode(spec.ExecutionTargets, snapshot.ExecutionMode) || !hasRequiredDomains(spec.RequiredDomains, domains) {
			continue
		}
		effective = append(effective, id)
	}

	sort.Strings(effective)
	return effective
}

func supportsMode(targets []agentprotocol.ToolSpecExecutionTargetsElem, mode agentprotocol.ExecutionMode) bool {
	var target agentprotocol.ToolSpecExecutionTargetsElem
	switch mode {
	case agentprotocol.ExecutionModeForeground:
		target = agentprotocol.ToolSpecExecutionTargetsElemClient
	case agentprotocol.ExecutionModeBackground:
		target = agentprotocol.ToolSpecExecutionTargetsElemServer
	default:
		return false
	}
	for _, candidate := range targets {
		if candidate == target {
			return true
		}
	}
	return false
}

func hasRequiredDomains(required []string, available map[string]struct{}) bool {
	for _, domain := range required {
		if _, found := available[domain]; !found {
			return false
		}
	}
	return true
}

func mustCopySpec(spec agentprotocol.ToolSpec) agentprotocol.ToolSpec {
	copied, err := copySpec(spec)
	if err != nil {
		panic(fmt.Sprintf("copy registered tool spec: %v", err))
	}
	return copied
}

func copySpec(spec agentprotocol.ToolSpec) (agentprotocol.ToolSpec, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return agentprotocol.ToolSpec{}, err
	}
	var copied agentprotocol.ToolSpec
	if err := json.Unmarshal(raw, &copied); err != nil {
		return agentprotocol.ToolSpec{}, err
	}
	return copied, nil
}
