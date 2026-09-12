package agentassets

import (
	"bytes"
	"slices"
	"testing"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentskill"
)

func TestCalendarReadSpecIsReadonlyOnBothHosts(t *testing.T) {
	spec, err := CalendarReadSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.ID != "dayorder.calendar.read" || spec.SideEffect != agentprotocol.SideEffectRead ||
		!slices.Equal(spec.RequiredDomains, []string{"calendar"}) ||
		!slices.Equal(spec.ExecutionTargets, []agentprotocol.ToolSpecExecutionTargetsElem{
			agentprotocol.ToolSpecExecutionTargetsElemClient,
			agentprotocol.ToolSpecExecutionTargetsElemServer,
		}) || spec.ApprovalPolicy != agentprotocol.ToolSpecApprovalPolicyNever || !spec.Idempotent ||
		spec.TimeoutMs != 10_000 || spec.ResultMaxBytes != 65_536 {
		t.Fatalf("calendar spec = %#v", spec)
	}
}

func TestCalendarOverviewBundleParsesWithTheRealProfileParser(t *testing.T) {
	spec, err := CalendarReadSpec()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := agentskill.ParseBundle(CalendarOverviewBundle())
	if err != nil {
		t.Fatal(err)
	}
	manifest := profile.Manifest
	if manifest.Name != "calendar-overview" || manifest.Version != "1.0.0" ||
		!slices.Equal(manifest.AllowedTools, []string{spec.ID}) ||
		manifest.ExecutionTarget != agentprotocol.SkillManifestExecutionTargetEither ||
		!manifest.BackgroundAllowed || manifest.MinRuntimeVersion != "2.0.0" ||
		manifest.RiskLevel != agentprotocol.SkillManifestRiskLevelLow ||
		profile.Descriptor.Scope != agentprotocol.SkillDescriptorScopeSystem || len(profile.SupportingFiles) != 0 {
		t.Fatalf("calendar profile = %#v", profile)
	}
}

func TestBuiltinAssetCallsReturnIsolatedCopies(t *testing.T) {
	firstSpec, err := CalendarReadSpec()
	if err != nil {
		t.Fatal(err)
	}
	firstBundle := CalendarOverviewBundle()
	firstSpec.ExecutionTargets[0] = agentprotocol.ToolSpecExecutionTargetsElemServer
	firstSpec.InputSchema["type"] = "array"
	for index := range firstBundle.SkillMarkdown {
		firstBundle.SkillMarkdown[index] = 0
	}
	firstBundle.SupportingFiles = append(firstBundle.SupportingFiles, agentskill.SupportingFile{Path: "references/injected.md"})

	secondSpec, err := CalendarReadSpec()
	if err != nil {
		t.Fatal(err)
	}
	secondBundle := CalendarOverviewBundle()
	if !slices.Equal(secondSpec.ExecutionTargets, []agentprotocol.ToolSpecExecutionTargetsElem{
		agentprotocol.ToolSpecExecutionTargetsElemClient,
		agentprotocol.ToolSpecExecutionTargetsElemServer,
	}) || secondSpec.InputSchema["type"] != "object" || !bytes.Contains(secondBundle.SkillMarkdown, []byte("name: calendar-overview")) ||
		len(secondBundle.SupportingFiles) != 0 {
		t.Fatalf("second spec/bundle were mutated: %#v / %#v", secondSpec, secondBundle)
	}
}
