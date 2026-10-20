package loops

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestInstructionPatchPreservesModelConfiguration(t *testing.T) {
	parent := Version{YAML: `models:
  custom:
    provider: openai
    model: test-model
    temperature: 0.2
    max_tokens: 1024
agents:
  root:
    model: custom
    description: Review assistant
    instruction: Cite files. Be concise. Never invent facts.
    max_iterations: 2
    skills: false
`}
	updated, err := applyInstructionPatch(parent, instructionPatch{
		BaseHash: configHash(parent.YAML), Rationale: "Use precise citations",
		Edits: []instructionEdit{{Old: "Cite files.", New: "Cite file paths and line numbers."}},
	}, "")
	require.NoError(t, err)
	var before, after agentConfig
	require.NoError(t, yaml.Unmarshal([]byte(parent.YAML), &before))
	require.NoError(t, yaml.Unmarshal([]byte(updated), &after))
	root := before.Agents["root"]
	root.Instruction = "Cite file paths and line numbers. Be concise. Never invent facts."
	before.Agents["root"] = root
	assert.Equal(t, before, after)
}

func TestInstructionPatchRejectsStaleAmbiguousAndBroadEdits(t *testing.T) {
	parent := Version{YAML: "agents:\n  root:\n    model: auto\n    instruction: " + strings.Repeat("Keep facts. ", 100) + "Be concise.\n"}
	for _, patch := range []instructionPatch{
		{BaseHash: "stale", Rationale: "Fix", Edits: []instructionEdit{{"Be concise.", "Be precise."}}},
		{BaseHash: configHash(parent.YAML), Rationale: "Fix", Edits: []instructionEdit{{"Keep facts.", "Invent facts."}}},
		{BaseHash: configHash(parent.YAML), Rationale: "Fix", Edits: []instructionEdit{{strings.Repeat("Keep facts. ", 100) + "Be concise.", "Entire replacement"}}},
		{BaseHash: configHash(parent.YAML), Rationale: "Fix", Edits: []instructionEdit{{"Be concise.", "${env.HOME}"}}},
	} {
		_, err := applyInstructionPatch(parent, patch, "")
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestPatchDesignerOnlyRequestsInstructionEdits(t *testing.T) {
	data, err := patchDesignerConfig("auto")
	require.NoError(t, err)
	assert.Contains(t, string(data), "instruction_patch")
	assert.Contains(t, string(data), "base_hash")
	assert.NotContains(t, string(data), "toolsets")
}

func TestInstructionPatchRejectsYAMLReferences(t *testing.T) {
	for _, config := range []string{
		"agents:\n  root:\n    model: auto\n    instruction: &text Cite files.\n    description: *text\n",
		"agents:\n  root:\n    <<: {model: auto}\n    instruction: Cite files.\n",
	} {
		parent := Version{YAML: config}
		_, err := applyInstructionPatch(parent, instructionPatch{BaseHash: configHash(config), Rationale: "fix", Edits: []instructionEdit{{"Cite files.", "Cite paths."}}}, "other/model")
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestInstructionPatchRequiresUniqueNonoverlappingOriginalAnchors(t *testing.T) {
	parent := Version{YAML: "agents:\n  root:\n    model: auto\n    instruction: abc aaa\n"}
	for _, edits := range [][]instructionEdit{
		{{"abc", "new"}, {"new", "again"}},
		{{"aa", "changed"}},
		{{"abc", "new"}, {"bc", "overlap"}},
	} {
		_, err := applyInstructionPatch(parent, instructionPatch{BaseHash: configHash(parent.YAML), Rationale: "fix", Edits: edits}, "")
		require.ErrorIs(t, err, ErrInvalid)
	}
}
