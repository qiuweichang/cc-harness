// Package catalog manages versioned agents, skills, rules, and knowledge bases.
package catalog

import "time"

const (
	// KindAgent identifies an intelligent role definition.
	KindAgent = "agents"
	// KindSkill identifies a reusable Claude Code skill.
	KindSkill = "skills"
	// KindRule identifies a task or member instruction rule.
	KindRule = "rules"
	// KindKnowledge identifies a local directory or MCP knowledge source.
	KindKnowledge = "knowledge-bases"
)

// Asset is the current catalog representation returned to the Web application.
// Body contains the selected version Markdown and Versions lists every immutable version.
type Asset struct {
	// SchemaVersion controls compatible asset.json decoding.
	SchemaVersion int `json:"schemaVersion"`
	// Revision detects conflicting metadata updates.
	Revision int64 `json:"revision"`
	// ID is the stable catalog directory key.
	ID string `json:"id"`
	// Kind is agents, skills, rules, or knowledge-bases.
	Kind string `json:"kind"`
	// Name is the user-facing asset name.
	Name string `json:"name"`
	// Description explains the asset's responsibility and intended use.
	Description string `json:"description"`
	// Avatar is an optional image URL used for agent presentation.
	Avatar string `json:"avatar,omitempty"`
	// Source is SYSTEM, USER, or AI.
	Source string `json:"source"`
	// Role is populated for agents and controls task-builder role matching.
	Role string `json:"role,omitempty"`
	// Enabled controls whether new tasks may select the asset.
	Enabled bool `json:"enabled"`
	// CurrentVersion selects the Markdown returned in Body.
	CurrentVersion string `json:"currentVersion"`
	// Versions lists available immutable versions in newest-first order.
	Versions []string `json:"versions"`
	// DefaultSkillIDs lists the skills automatically assigned to an agent.
	DefaultSkillIDs []string `json:"defaultSkillIds,omitempty"`
	// Config stores knowledge-base type, path, or MCP settings without secrets.
	Config map[string]any `json:"config,omitempty"`
	// Body is loaded from versions/{version}.md and is not duplicated in asset.json.
	Body string `json:"body"`
	// CreatedAt is the UTC creation time.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is the UTC metadata update time.
	UpdatedAt time.Time `json:"updatedAt"`
}

// Metadata is the small mutable asset.json document stored beside version files.
type Metadata struct {
	SchemaVersion   int            `json:"schemaVersion"`
	Revision        int64          `json:"revision"`
	ID              string         `json:"id"`
	Kind            string         `json:"kind"`
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	Avatar          string         `json:"avatar,omitempty"`
	Source          string         `json:"source"`
	Role            string         `json:"role,omitempty"`
	Enabled         bool           `json:"enabled"`
	CurrentVersion  string         `json:"currentVersion"`
	DefaultSkillIDs []string       `json:"defaultSkillIds,omitempty"`
	Config          map[string]any `json:"config,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

// UpsertInput contains editable catalog metadata and Markdown content.
type UpsertInput struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	Avatar          string         `json:"avatar"`
	Source          string         `json:"source"`
	Role            string         `json:"role"`
	Enabled         *bool          `json:"enabled,omitempty"`
	Version         string         `json:"version"`
	DefaultSkillIDs []string       `json:"defaultSkillIds"`
	Config          map[string]any `json:"config"`
	Body            string         `json:"body"`
}

// VersionInput creates a new immutable Markdown version and optionally selects it.
type VersionInput struct {
	Version string `json:"version"`
	Body    string `json:"body"`
	Select  bool   `json:"select"`
}

// RelationSummary reports tasks and agents that currently reference an asset.
type RelationSummary struct {
	AgentIDs []string `json:"agentIds"`
	TaskIDs  []string `json:"taskIds"`
}
