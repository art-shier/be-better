// Package agentskill parses explicitly supplied Skill bundles and exposes
// their inert metadata to the background agent runtime.
package agentskill

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"dayorder.local/api/internal/agentprotocol"
	"gopkg.in/yaml.v3"
)

const (
	maxBundleFiles = 32
	maxFileBytes   = 256 * 1024
	maxBundleBytes = 1024 * 1024
)

var frontmatterPattern = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---\r?\n(.*)$`)

// SupportingFile is static content supplied by the caller as part of a Skill
// bundle. ParseBundle never discovers files from disk.
type SupportingFile struct {
	Path    string
	Content []byte
}

// SkillBundleInput contains every byte required to parse one Skill.
type SkillBundleInput struct {
	Scope           agentprotocol.SkillDescriptorScope
	SkillMarkdown   []byte
	SupportingFiles []SupportingFile
}

// ProfileSupportingFile is a copied static file with wire metadata.
type ProfileSupportingFile struct {
	Path    string
	Kind    agentprotocol.SupportingFileDescriptorKind
	Size    int
	Content []byte
}

// SkillProfile is the validated manifest, disclosure body, descriptor, and
// copied static supporting content for one Skill.
type SkillProfile struct {
	Manifest        agentprotocol.SkillManifest
	Descriptor      agentprotocol.SkillDescriptor
	Body            string
	SupportingFiles []ProfileSupportingFile
}

type checkedFile struct {
	source SupportingFile
	path   string
	kind   agentprotocol.SupportingFileDescriptorKind
}

// ParseBundle validates an explicitly provided, system-scoped static Skill
// bundle. It performs no filesystem discovery and executes no bundle content.
func ParseBundle(input SkillBundleInput) (SkillProfile, error) {
	if input.Scope != agentprotocol.SkillDescriptorScopeSystem {
		return SkillProfile{}, fmt.Errorf("phase one accepts system Skills only")
	}
	checked, err := checkedPaths(input.SupportingFiles)
	if err != nil {
		return SkillProfile{}, err
	}

	skillMarkdown := bytes.Clone(input.SkillMarkdown)
	manifest, body, err := splitSkillMarkdown(skillMarkdown)
	if err != nil {
		return SkillProfile{}, err
	}
	files, err := copyAndLimitFiles(checked, len(skillMarkdown))
	if err != nil {
		return SkillProfile{}, err
	}

	digest := digestBundle(skillMarkdown, files)
	descriptor := agentprotocol.SkillDescriptor{
		Name:                   manifest.Name,
		Version:                manifest.Version,
		Digest:                 digest,
		Description:            manifest.Description,
		Scope:                  input.Scope,
		ExecutionTarget:        agentprotocol.SkillDescriptorExecutionTarget(manifest.ExecutionTarget),
		BackgroundAllowed:      manifest.BackgroundAllowed,
		UserInvocable:          manifest.UserInvocable,
		DisableModelInvocation: manifest.DisableModelInvocation,
		RiskLevel:              agentprotocol.SkillDescriptorRiskLevel(manifest.RiskLevel),
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionSkillDescriptor, descriptor); err != nil {
		return SkillProfile{}, err
	}
	return SkillProfile{Manifest: manifest, Descriptor: descriptor, Body: body, SupportingFiles: files}, nil
}

func checkedPaths(files []SupportingFile) ([]checkedFile, error) {
	if len(files)+1 > maxBundleFiles {
		return nil, fmt.Errorf("too many bundle files (limit 32 including SKILL.md)")
	}
	checked := make([]checkedFile, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, source := range files {
		path, kind, err := checkedSupportingPath(source.Path)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[path]; duplicate {
			return nil, fmt.Errorf("duplicate supporting file path: %s", path)
		}
		seen[path] = struct{}{}
		checked = append(checked, checkedFile{source: source, path: path, kind: kind})
	}
	sort.Slice(checked, func(i, j int) bool { return checked[i].path < checked[j].path })
	return checked, nil
}

func checkedSupportingPath(original string) (string, agentprotocol.SupportingFileDescriptorKind, error) {
	if original == "" || strings.ContainsRune(original, '\x00') || !utf8.ValidString(original) {
		return "", "", fmt.Errorf("invalid supporting file path")
	}
	slashed := strings.ReplaceAll(original, `\`, "/")
	if strings.HasPrefix(slashed, "/") || isWindowsDriveAbsolute(slashed) {
		return "", "", fmt.Errorf("absolute supporting file path is forbidden: %s", original)
	}
	segments := strings.Split(slashed, "/")
	for _, segment := range segments {
		if segment == ".." {
			return "", "", fmt.Errorf("parent traversal is forbidden: %s", original)
		}
		if segment == "" {
			return "", "", fmt.Errorf("invalid supporting file path: %s", original)
		}
	}
	normalizedSegments := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment != "." {
			normalizedSegments = append(normalizedSegments, segment)
		}
	}
	if len(normalizedSegments) < 2 {
		return "", "", fmt.Errorf("supporting file must be below a static directory: %s", original)
	}
	path := strings.Join(normalizedSegments, "/")
	root := normalizedSegments[0]
	if root == "scripts" {
		return "", "", fmt.Errorf("scripts/ paths are forbidden: %s", original)
	}
	var kind agentprotocol.SupportingFileDescriptorKind
	switch root {
	case "references":
		kind = agentprotocol.SupportingFileDescriptorKindReference
	case "assets":
		kind = agentprotocol.SupportingFileDescriptorKindAsset
	case "schemas":
		kind = agentprotocol.SupportingFileDescriptorKindSchema
	default:
		return "", "", fmt.Errorf("unsupported supporting file path: %s", original)
	}
	return path, kind, nil
}

func isWindowsDriveAbsolute(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	letter := path[0]
	if !((letter >= 'A' && letter <= 'Z') || (letter >= 'a' && letter <= 'z')) {
		return false
	}
	return len(path) == 2 || path[2] == '/'
}

func copyAndLimitFiles(checked []checkedFile, skillMarkdownBytes int) ([]ProfileSupportingFile, error) {
	total := skillMarkdownBytes
	if total > maxFileBytes {
		return nil, fmt.Errorf("SKILL.md is too large")
	}
	files := make([]ProfileSupportingFile, 0, len(checked))
	for _, file := range checked {
		if len(file.source.Content) > maxFileBytes {
			return nil, fmt.Errorf("supporting file is too large: %s", file.path)
		}
		total += len(file.source.Content)
		if total > maxBundleBytes {
			return nil, fmt.Errorf("bundle files exceed total size limit")
		}
		content := bytes.Clone(file.source.Content)
		files = append(files, ProfileSupportingFile{Path: file.path, Kind: file.kind, Size: len(content), Content: content})
	}
	return files, nil
}

func splitSkillMarkdown(raw []byte) (agentprotocol.SkillManifest, string, error) {
	if len(raw) > maxFileBytes {
		return agentprotocol.SkillManifest{}, "", fmt.Errorf("SKILL.md is too large")
	}
	if !utf8.Valid(raw) {
		return agentprotocol.SkillManifest{}, "", fmt.Errorf("SKILL.md must be valid UTF-8")
	}
	matched := frontmatterPattern.FindSubmatch(raw)
	if matched == nil {
		return agentprotocol.SkillManifest{}, "", fmt.Errorf("SKILL.md requires anchored YAML Frontmatter and a body")
	}
	body := string(matched[2])
	if strings.TrimSpace(body) == "" {
		return agentprotocol.SkillManifest{}, "", fmt.Errorf("SKILL.md body is required")
	}

	var parsed any
	decoder := yaml.NewDecoder(bytes.NewReader(matched[1]))
	if err := decoder.Decode(&parsed); err != nil {
		return agentprotocol.SkillManifest{}, "", fmt.Errorf("parse Skill Frontmatter: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return agentprotocol.SkillManifest{}, "", fmt.Errorf("parse Skill Frontmatter: %w", err)
		}
		return agentprotocol.SkillManifest{}, "", fmt.Errorf("Skill Frontmatter must contain exactly one YAML document")
	}
	normalized, err := normalizeYAML(parsed)
	if err != nil {
		return agentprotocol.SkillManifest{}, "", err
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionSkillManifest, normalized); err != nil {
		return agentprotocol.SkillManifest{}, "", err
	}
	rawManifest, err := json.Marshal(normalized)
	if err != nil {
		return agentprotocol.SkillManifest{}, "", fmt.Errorf("encode Skill manifest: %w", err)
	}
	var manifest agentprotocol.SkillManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		return agentprotocol.SkillManifest{}, "", fmt.Errorf("decode Skill manifest: %w", err)
	}
	return manifest, body, nil
}

func normalizeYAML(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			normalized, err := normalizeYAML(child)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case map[any]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			stringKey, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("Skill Frontmatter mapping keys must be strings")
			}
			normalized, err := normalizeYAML(child)
			if err != nil {
				return nil, err
			}
			result[stringKey] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			normalized, err := normalizeYAML(child)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return nil, fmt.Errorf("Skill Frontmatter contains a non-JSON value: %w", err)
		}
		var normalized any
		if err := json.Unmarshal(raw, &normalized); err != nil {
			return nil, fmt.Errorf("normalize Skill Frontmatter value: %w", err)
		}
		return normalized, nil
	}
}

func digestBundle(skillMarkdown []byte, files []ProfileSupportingFile) string {
	digest := sha256.New()
	_, _ = digest.Write(skillMarkdown)
	for _, file := range files {
		_, _ = digest.Write([]byte(file.Path))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write(file.Content)
	}
	return hex.EncodeToString(digest.Sum(nil))
}
