package agentskill

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dayorder.local/api/internal/agentprotocol"
)

const goldenDigest = "f2c538af41506502037bc4a1d83e4abb220b2ed1bb8207d461793ef257dee739"

func fixtureBytes(t *testing.T, relative ...string) []byte {
	t.Helper()
	parts := append([]string{"..", "..", "..", "..", "contracts", "agent", "fixtures", "skills"}, relative...)
	value, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func validBundle(t *testing.T) SkillBundleInput {
	t.Helper()
	return SkillBundleInput{
		Scope:         agentprotocol.SkillDescriptorScopeSystem,
		SkillMarkdown: fixtureBytes(t, "calendar-management", "SKILL.md"),
		SupportingFiles: []SupportingFile{{
			Path:    "references/usage.md",
			Content: fixtureBytes(t, "calendar-management", "references", "usage.md"),
		}},
	}
}

func markdownWith(t *testing.T, from, to string) []byte {
	t.Helper()
	return bytes.Replace(validBundle(t).SkillMarkdown, []byte(from), []byte(to), 1)
}

func TestParseBundleKeepsGoldenDigestAndSortsCopiedFiles(t *testing.T) {
	bundle := validBundle(t)
	bundle.SupportingFiles = []SupportingFile{
		{Path: "schemas/z.json", Content: []byte("{}\n")},
		{Path: "references/usage.md", Content: fixtureBytes(t, "calendar-management", "references", "usage.md")},
		{Path: "assets/icon.bin", Content: []byte{2, 1}},
	}
	profile, err := ParseBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := ParseBundle(validBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	if golden.Descriptor.Digest != goldenDigest {
		t.Fatalf("digest = %q, want %q", golden.Descriptor.Digest, goldenDigest)
	}
	if golden.Descriptor.Name != "calendar-management" || golden.Descriptor.Version != "1.0.0" ||
		golden.Descriptor.Description != "Read and propose changes to DayOrder calendar data." ||
		golden.Descriptor.Scope != agentprotocol.SkillDescriptorScopeSystem ||
		golden.Descriptor.ExecutionTarget != agentprotocol.SkillDescriptorExecutionTargetEither ||
		!golden.Descriptor.BackgroundAllowed || !golden.Descriptor.UserInvocable || golden.Descriptor.DisableModelInvocation ||
		golden.Descriptor.RiskLevel != agentprotocol.SkillDescriptorRiskLevelMedium {
		t.Fatalf("descriptor = %#v", golden.Descriptor)
	}
	wantPaths := []string{"assets/icon.bin", "references/usage.md", "schemas/z.json"}
	gotPaths := []string{profile.SupportingFiles[0].Path, profile.SupportingFiles[1].Path, profile.SupportingFiles[2].Path}
	if !slices.Equal(gotPaths, wantPaths) {
		t.Fatalf("supporting paths = %v, want %v", gotPaths, wantPaths)
	}
	if profile.SupportingFiles[0].Kind != agentprotocol.SupportingFileDescriptorKindAsset ||
		profile.SupportingFiles[1].Kind != agentprotocol.SupportingFileDescriptorKindReference ||
		profile.SupportingFiles[2].Kind != agentprotocol.SupportingFileDescriptorKindSchema ||
		!bytes.Equal(profile.SupportingFiles[0].Content, []byte{2, 1}) {
		t.Fatalf("supporting files = %#v", profile.SupportingFiles)
	}
}

func TestParseBundleNormalizesAndSortsPathsForDigest(t *testing.T) {
	first := validBundle(t)
	first.SupportingFiles = []SupportingFile{{Path: "schemas/b.json", Content: []byte("b")}, {Path: "./references/a.md", Content: []byte("a")}}
	second := validBundle(t)
	second.SupportingFiles = []SupportingFile{{Path: "references/a.md", Content: []byte("a")}, {Path: "schemas/b.json", Content: []byte("b")}}
	a, err := ParseBundle(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseBundle(second)
	if err != nil {
		t.Fatal(err)
	}
	if a.Descriptor.Digest != b.Descriptor.Digest {
		t.Fatalf("digests differ: %s != %s", a.Descriptor.Digest, b.Descriptor.Digest)
	}
	if got := []string{a.SupportingFiles[0].Path, a.SupportingFiles[1].Path}; !slices.Equal(got, []string{"references/a.md", "schemas/b.json"}) {
		t.Fatalf("paths = %v", got)
	}
}

func TestParseBundleUsesUTF8ByteOrdering(t *testing.T) {
	bundle := validBundle(t)
	bundle.SupportingFiles = []SupportingFile{
		{Path: "references/😺.md", Content: []byte("emoji")},
		{Path: "references/\ue000.md", Content: []byte("private-use")},
		{Path: "references/é.md", Content: []byte("accent")},
		{Path: "references/z.md", Content: []byte("ascii")},
	}
	profile, err := ParseBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(profile.SupportingFiles))
	for i := range profile.SupportingFiles {
		got[i] = profile.SupportingFiles[i].Path
	}
	want := []string{"references/z.md", "references/é.md", "references/\ue000.md", "references/😺.md"}
	if !slices.Equal(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestParseBundleRejectsInvalidMarkdown(t *testing.T) {
	valid := string(validBundle(t).SkillMarkdown)
	cases := map[string][]byte{
		"missing frontmatter":    []byte("# Calendar Management\n\nBody.\n"),
		"missing body":           []byte(strings.Replace(valid, "# Calendar Management\n\nRead only the requested date range.\nTreat calendar writes as proposals and never apply a change without the required confirmation.\n", "", 1)),
		"invalid name":           markdownWith(t, "name: calendar-management", "name: Calendar_Management"),
		"invalid semver":         markdownWith(t, "version: 1.0.0", "version: 01.0.0"),
		"unknown field":          markdownWith(t, "risk-level: medium", "risk-level: medium\nscope: user"),
		"duplicate yaml key":     markdownWith(t, "name: calendar-management", "name: calendar-management\nname: duplicate"),
		"non-string mapping key": markdownWith(t, "risk-level: medium", "risk-level: medium\n? [one, two]\n: value"),
	}
	for name, markdown := range cases {
		t.Run(name, func(t *testing.T) {
			bundle := validBundle(t)
			bundle.SkillMarkdown = markdown
			if _, err := ParseBundle(bundle); err == nil {
				t.Fatal("ParseBundle() succeeded")
			}
		})
	}
}

func TestParseBundleRejectsACommentDelimitedSecondYAMLDocument(t *testing.T) {
	bundle := validBundle(t)
	bundle.SkillMarkdown = markdownWith(
		t,
		"risk-level: medium\n---\n# Calendar Management",
		"risk-level: medium\n...\n--- # second\nscope: user\n---\n# Calendar Management",
	)

	if _, err := ParseBundle(bundle); err == nil {
		t.Fatal("ParseBundle() accepted a second Frontmatter YAML document")
	}
}

func TestParseBundleRejectsUnsafeAndDuplicatePaths(t *testing.T) {
	paths := map[string]string{
		"posix absolute":             "/references/usage.md",
		"windows drive absolute":     `C:\references\usage.md`,
		"unc absolute":               `\\server\share\usage.md`,
		"parent traversal":           "references/../usage.md",
		"backslash parent traversal": `references\..\usage.md`,
		"unsupported root":           "other/usage.md",
		"scripts root":               "scripts/run.js",
		"normalized scripts root":    "./scripts/run.js",
		"backslash scripts root":     `scripts\run.js`,
	}
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			bundle := validBundle(t)
			content := bytes.Repeat([]byte{'x'}, 256*1024+1)
			if strings.Contains(name, "scripts") {
				bundle.SkillMarkdown = fixtureBytes(t, "invalid-script", "SKILL.md")
				content = fixtureBytes(t, "invalid-script", "scripts", "run.js")
			}
			bundle.SupportingFiles = []SupportingFile{{Path: path, Content: content}}
			_, err := ParseBundle(bundle)
			if err == nil {
				t.Fatal("ParseBundle() succeeded")
			}
			if strings.Contains(name, "scripts") && !strings.Contains(err.Error(), "scripts") {
				t.Fatalf("error = %v, want scripts path rejection before size validation", err)
			}
		})
	}

	bundle := validBundle(t)
	bundle.SupportingFiles = []SupportingFile{{Path: "references/usage.md", Content: []byte("a")}, {Path: `.\references\usage.md`, Content: []byte("b")}}
	if _, err := ParseBundle(bundle); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate path error = %v", err)
	}
}

func TestParseBundleEnforcesFileCountAndByteLimits(t *testing.T) {
	bundle := validBundle(t)
	bundle.SupportingFiles = make([]SupportingFile, 31)
	for i := range bundle.SupportingFiles {
		bundle.SupportingFiles[i] = SupportingFile{Path: "references/" + string(rune('a'+i)) + ".md", Content: []byte{}}
	}
	if _, err := ParseBundle(bundle); err != nil {
		t.Fatalf("31 supporting files: %v", err)
	}
	bundle.SupportingFiles = append(bundle.SupportingFiles, SupportingFile{Path: "references/overflow.md", Content: []byte{}})
	if _, err := ParseBundle(bundle); err == nil {
		t.Fatal("32 supporting files succeeded")
	}

	bundle = validBundle(t)
	sizes := []int{256 * 1024, 256 * 1024, 256 * 1024, 261_648}
	bundle.SupportingFiles = make([]SupportingFile, len(sizes))
	for i, size := range sizes {
		bundle.SupportingFiles[i] = SupportingFile{Path: "assets/" + string(rune('a'+i)) + ".bin", Content: make([]byte, size)}
	}
	if _, err := ParseBundle(bundle); err != nil {
		t.Fatalf("exact aggregate limit: %v", err)
	}
	bundle.SupportingFiles[3].Content = make([]byte, 261_649)
	if _, err := ParseBundle(bundle); err == nil || !strings.Contains(err.Error(), "total size limit") {
		t.Fatalf("over aggregate limit error = %v", err)
	}

	bundle = validBundle(t)
	bundle.SupportingFiles = []SupportingFile{{Path: "assets/large.bin", Content: make([]byte, 256*1024+1)}}
	if _, err := ParseBundle(bundle); err == nil || !strings.Contains(err.Error(), "large") {
		t.Fatalf("over file limit error = %v", err)
	}

	bundle = validBundle(t)
	bundle.SkillMarkdown = make([]byte, 256*1024+1)
	if _, err := ParseBundle(bundle); err == nil || !strings.Contains(err.Error(), "SKILL.md") {
		t.Fatalf("over SKILL.md limit error = %v", err)
	}
}

func TestParseBundleRejectsNonSystemScopeAndCopiesBytes(t *testing.T) {
	bundle := validBundle(t)
	bundle.Scope = agentprotocol.SkillDescriptorScopeUser
	if _, err := ParseBundle(bundle); err == nil || !strings.Contains(err.Error(), "system") {
		t.Fatalf("user scope error = %v", err)
	}

	bundle = validBundle(t)
	profile, err := ParseBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	clear(bundle.SkillMarkdown)
	clear(bundle.SupportingFiles[0].Content)
	if !strings.Contains(profile.Body, "Read only the requested date range.") || string(profile.SupportingFiles[0].Content) != "# Usage\n\nUse `dayorder.calendar.read` before proposing a calendar change.\n" {
		t.Fatalf("profile changed through caller alias: %#v", profile)
	}
}

func TestCanonicalFixturesPreserveFinalBytes(t *testing.T) {
	if got := fixtureBytes(t, "calendar-management", "SKILL.md"); len(got) != 496 || got[len(got)-1] != '\n' {
		t.Fatalf("SKILL.md length/final byte = %d/%v", len(got), got[len(got)-1])
	}
	if got := string(fixtureBytes(t, "calendar-management", "references", "usage.md")); got != "# Usage\n\nUse `dayorder.calendar.read` before proposing a calendar change.\n" {
		t.Fatalf("usage fixture = %q", got)
	}
	if got := string(fixtureBytes(t, "invalid-script", "scripts", "run.js")); got != "throw new Error(\"must not execute\");\n" {
		t.Fatalf("script fixture = %q", got)
	}
}
