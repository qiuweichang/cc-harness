package configset

import (
	"errors"
	"os"
	"path/filepath"

	"cc-hardness/internal/filedata"
)

// Repository maps configuration sets to one atomically replaced JSON index.
type Repository struct {
	// path is the canonical configuration-set index below the data root.
	path string
}

// NewRepository prepares the fixed configuration directory and index location.
func NewRepository(dataRoot string) (*Repository, error) {
	directory := filepath.Join(dataRoot, "config")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, err
	}
	return &Repository{path: filepath.Join(directory, "config-sets.json")}, nil
}

// Load returns every persisted set; an absent index represents an empty catalog.
func (r *Repository) Load() ([]Set, error) {
	var index indexFile
	if err := filedata.ReadJSON(r.path, &index); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Set{}, nil
		}
		return nil, err
	}
	if index.SchemaVersion != 1 {
		return nil, errors.New("configuration set index uses an unsupported schema")
	}
	if index.Sets == nil {
		index.Sets = []Set{}
	}
	return index.Sets, nil
}

// Save atomically replaces the complete index so readers never observe partial edits.
func (r *Repository) Save(sets []Set) error {
	return filedata.WriteJSON(r.path, indexFile{SchemaVersion: 1, Sets: sets})
}
