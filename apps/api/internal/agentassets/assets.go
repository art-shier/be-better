// Package agentassets exposes validated, isolated copies of product-owned
// builtin Agent assets without registering them in a runtime.
package agentassets

import (
	"bytes"
	_ "embed"
	"encoding/json"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentskill"
)

//go:embed generated/tools/calendar-read.json
var calendarReadJSON []byte

//go:embed generated/skills/calendar-overview/SKILL.md
var calendarOverviewMarkdown []byte

// CalendarReadSpec returns a validated copy of the builtin calendar ToolSpec.
func CalendarReadSpec() (agentprotocol.ToolSpec, error) {
	var spec agentprotocol.ToolSpec
	if err := json.Unmarshal(calendarReadJSON, &spec); err != nil {
		return agentprotocol.ToolSpec{}, err
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionToolSpec, spec); err != nil {
		return agentprotocol.ToolSpec{}, err
	}
	return spec, nil
}

// CalendarOverviewBundle returns an isolated system-scoped Skill bundle.
func CalendarOverviewBundle() agentskill.SkillBundleInput {
	return agentskill.SkillBundleInput{
		Scope:           agentprotocol.SkillDescriptorScopeSystem,
		SkillMarkdown:   bytes.Clone(calendarOverviewMarkdown),
		SupportingFiles: []agentskill.SupportingFile{},
	}
}
