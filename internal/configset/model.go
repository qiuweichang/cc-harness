// Package configset manages reusable local configuration collections that can
// be attached to task plans without duplicating environment-specific values.
package configset

import "time"

// Entry is one named raw configuration document, such as redis, mysql, or oss.
type Entry struct {
	// ID is stable within its parent set and supports precise UI updates.
	ID string `json:"id"`
	// Name is the middleware or service lookup key used by development agents.
	Name string `json:"name"`
	// Content preserves the user's YAML, properties, JSON, or text verbatim.
	Content string `json:"content"`
}

// Set is one reusable configuration collection persisted in the local data root.
type Set struct {
	// SchemaVersion controls compatible file decoding.
	SchemaVersion int `json:"schemaVersion"`
	// Revision increments on each edit for durable change tracking.
	Revision int64 `json:"revision"`
	// ID is the immutable task-plan reference.
	ID string `json:"id"`
	// Name is the user-facing set label.
	Name string `json:"name"`
	// Description explains the intended environment or project scope.
	Description string `json:"description"`
	// Entries contains named configuration documents in display order.
	Entries []Entry `json:"entries"`
	// CreatedAt is the original UTC creation time.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is the latest UTC edit time.
	UpdatedAt time.Time `json:"updatedAt"`
}

// UpsertInput contains all user-editable fields for create and update requests.
type UpsertInput struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Entries     []Entry `json:"entries"`
}

// indexFile is the complete atomic persistence document.
type indexFile struct {
	SchemaVersion int   `json:"schemaVersion"`
	Sets          []Set `json:"sets"`
}
