package agentskill

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agenttool"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Registry stores validated, independent Skill profiles. After construction it
// is immutable and safe for concurrent readers.
type Registry struct {
	profiles map[string]SkillProfile
}

// NewRegistry validates and copies profiles, rejecting duplicate Skill names.
func NewRegistry(profiles []SkillProfile) (*Registry, error) {
	registry := &Registry{profiles: make(map[string]SkillProfile, len(profiles))}
	for _, source := range profiles {
		profile, err := copyProfile(source)
		if err != nil {
			return nil, err
		}
		if err := assertProfile(profile); err != nil {
			return nil, err
		}
		if _, exists := registry.profiles[profile.Descriptor.Name]; exists {
			return nil, fmt.Errorf("duplicate Skill name: %s", profile.Descriptor.Name)
		}
		registry.profiles[profile.Descriptor.Name] = profile
	}
	return registry, nil
}

// List returns descriptor copies sorted by Skill name. Instructions remain
// undisclosed until Load or Activate.
func (r *Registry) List() []agentprotocol.SkillDescriptor {
	descriptors := make([]agentprotocol.SkillDescriptor, 0, len(r.profiles))
	for _, profile := range r.profiles {
		descriptors = append(descriptors, profile.Descriptor)
	}
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].Name < descriptors[j].Name })
	return descriptors
}

// ListForModel returns only Skills that permit model invocation.
func (r *Registry) ListForModel() []agentprotocol.SkillDescriptor {
	all := r.List()
	visible := make([]agentprotocol.SkillDescriptor, 0, len(all))
	for _, descriptor := range all {
		if !descriptor.DisableModelInvocation {
			visible = append(visible, descriptor)
		}
	}
	return visible
}

// Load returns an independent profile copy for name.
func (r *Registry) Load(name string) (SkillProfile, bool) {
	profile, ok := r.profiles[name]
	if !ok {
		return SkillProfile{}, false
	}
	copied, err := copyProfile(profile)
	if err != nil {
		return SkillProfile{}, false
	}
	return copied, true
}

// LoadForModel returns an independent copy only when model invocation is
// enabled for the Skill.
func (r *Registry) LoadForModel(name string) (SkillProfile, bool) {
	profile, ok := r.profiles[name]
	if !ok || profile.Descriptor.DisableModelInvocation {
		return SkillProfile{}, false
	}
	copied, err := copyProfile(profile)
	if err != nil {
		return SkillProfile{}, false
	}
	return copied, true
}

// Activate validates runtime compatibility and intersects requested Tools with
// registered bindings, Run Scope, execution mode, and system policy. A Skill
// request never grants Tool permission.
func (r *Registry) Activate(name string, snapshot agentprotocol.CapabilitySnapshot, tools *agenttool.Registry, policy agenttool.Policy) (agentprotocol.SkillActivation, error) {
	profile, ok := r.profiles[name]
	if !ok {
		return agentprotocol.SkillActivation{}, fmt.Errorf("Skill not found: %s", name)
	}
	if err := assertCompatible(profile, snapshot); err != nil {
		return agentprotocol.SkillActivation{}, err
	}
	requested := append([]string(nil), profile.Manifest.AllowedTools...)
	sort.Strings(requested)
	requested = compactStrings(requested)
	active := agenttool.EffectiveToolIDs(requested, tools, snapshot, policy)
	if active == nil {
		active = []string{}
	}
	supporting := make([]agentprotocol.SupportingFileDescriptor, len(profile.SupportingFiles))
	for index, file := range profile.SupportingFiles {
		supporting[index] = agentprotocol.SupportingFileDescriptor{Path: file.Path, Kind: file.Kind, Size: file.Size}
	}
	activation := agentprotocol.SkillActivation{
		Skill:        agentprotocol.SkillRef{Name: profile.Descriptor.Name, Version: profile.Descriptor.Version, Digest: profile.Descriptor.Digest},
		Instructions: profile.Body, SupportingFiles: supporting, RequestedToolIds: requested, ActiveToolIds: active,
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionSkillActivation, activation); err != nil {
		return agentprotocol.SkillActivation{}, err
	}
	return activation, nil
}

func copyProfile(profile SkillProfile) (SkillProfile, error) {
	raw, err := json.Marshal(profile)
	if err != nil {
		return SkillProfile{}, fmt.Errorf("copy Skill profile: %w", err)
	}
	var copied SkillProfile
	if err := json.Unmarshal(raw, &copied); err != nil {
		return SkillProfile{}, fmt.Errorf("copy Skill profile: %w", err)
	}
	if copied.SupportingFiles == nil {
		copied.SupportingFiles = []ProfileSupportingFile{}
	}
	return copied, nil
}

func assertProfile(profile SkillProfile) error {
	if err := agentprotocol.Validate(agentprotocol.DefinitionSkillManifest, profile.Manifest); err != nil {
		return err
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionSkillDescriptor, profile.Descriptor); err != nil {
		return err
	}
	if profile.Descriptor.Scope != agentprotocol.SkillDescriptorScopeSystem {
		return fmt.Errorf("phase one accepts system Skills only")
	}
	if !digestPattern.MatchString(profile.Descriptor.Digest) {
		return fmt.Errorf("invalid Skill digest")
	}
	if profile.Descriptor.Name != profile.Manifest.Name || profile.Descriptor.Version != profile.Manifest.Version ||
		profile.Descriptor.Description != profile.Manifest.Description ||
		profile.Descriptor.ExecutionTarget != agentprotocol.SkillDescriptorExecutionTarget(profile.Manifest.ExecutionTarget) ||
		profile.Descriptor.BackgroundAllowed != profile.Manifest.BackgroundAllowed ||
		profile.Descriptor.UserInvocable != profile.Manifest.UserInvocable ||
		profile.Descriptor.DisableModelInvocation != profile.Manifest.DisableModelInvocation ||
		profile.Descriptor.RiskLevel != agentprotocol.SkillDescriptorRiskLevel(profile.Manifest.RiskLevel) {
		return fmt.Errorf("Skill profile descriptor does not match its manifest")
	}
	return nil
}

func assertCompatible(profile SkillProfile, snapshot agentprotocol.CapabilitySnapshot) error {
	if compareSemVer(snapshot.RuntimeVersion, string(profile.Manifest.MinRuntimeVersion)) < 0 {
		return fmt.Errorf("Skill requires Runtime %s", profile.Manifest.MinRuntimeVersion)
	}
	requiredTarget := agentprotocol.SkillDescriptorExecutionTargetClient
	if snapshot.ExecutionMode == agentprotocol.ExecutionModeBackground {
		requiredTarget = agentprotocol.SkillDescriptorExecutionTargetServer
	}
	if profile.Descriptor.ExecutionTarget != agentprotocol.SkillDescriptorExecutionTargetEither && profile.Descriptor.ExecutionTarget != requiredTarget {
		return fmt.Errorf("Skill does not support %s execution", snapshot.ExecutionMode)
	}
	if snapshot.ExecutionMode == agentprotocol.ExecutionModeBackground && !profile.Descriptor.BackgroundAllowed {
		return fmt.Errorf("Skill does not allow background execution")
	}
	return nil
}

type parsedVersion struct {
	core       [3]*big.Int
	prerelease []string
}

func parseVersion(value string) parsedVersion {
	withoutBuild := strings.SplitN(value, "+", 2)[0]
	pieces := strings.SplitN(withoutBuild, "-", 2)
	coreStrings := strings.Split(pieces[0], ".")
	parsed := parsedVersion{}
	for i := range parsed.core {
		parsed.core[i] = new(big.Int)
		if i < len(coreStrings) {
			parsed.core[i].SetString(coreStrings[i], 10)
		}
	}
	if len(pieces) == 2 {
		parsed.prerelease = strings.Split(pieces[1], ".")
	}
	return parsed
}

func compareSemVer(left, right string) int {
	a, b := parseVersion(left), parseVersion(right)
	for i := 0; i < 3; i++ {
		if comparison := a.core[i].Cmp(b.core[i]); comparison != 0 {
			return comparison
		}
	}
	if len(a.prerelease) == 0 || len(b.prerelease) == 0 {
		switch {
		case len(a.prerelease) == len(b.prerelease):
			return 0
		case len(a.prerelease) == 0:
			return 1
		default:
			return -1
		}
	}
	for i := 0; i < len(a.prerelease) || i < len(b.prerelease); i++ {
		if i == len(a.prerelease) {
			return -1
		}
		if i == len(b.prerelease) {
			return 1
		}
		if a.prerelease[i] == b.prerelease[i] {
			continue
		}
		leftNumber, leftOK := new(big.Int).SetString(a.prerelease[i], 10)
		rightNumber, rightOK := new(big.Int).SetString(b.prerelease[i], 10)
		switch {
		case leftOK && rightOK:
			return leftNumber.Cmp(rightNumber)
		case leftOK:
			return -1
		case rightOK:
			return 1
		case a.prerelease[i] < b.prerelease[i]:
			return -1
		default:
			return 1
		}
	}
	return 0
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
