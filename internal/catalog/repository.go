package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cc-hardness/internal/filedata"
)

var (
	// ErrNotFound indicates that the requested catalog directory does not exist.
	ErrNotFound = errors.New("catalog asset not found")
	// ErrConflict indicates that a version or identifier already exists.
	ErrConflict = errors.New("catalog asset conflict")
)

// Repository maps catalog assets to one directory per stable ID.
type Repository struct {
	// root is data/catalog and contains one subdirectory for each asset kind.
	root string
	// mu serializes metadata and version writes within the local process.
	mu sync.RWMutex
}

// NewRepository creates the catalog directories and returns a file repository.
func NewRepository(dataRoot string) (*Repository, error) {
	root := filepath.Join(dataRoot, "catalog")
	for _, kind := range []string{KindAgent, KindSkill, KindRule, KindKnowledge} {
		if err := os.MkdirAll(filepath.Join(root, kind), 0o755); err != nil {
			return nil, err
		}
	}
	return &Repository{root: root}, nil
}

// List returns all assets of kind with their currently selected Markdown body.
func (r *Repository) List(kind string) ([]Asset, error) {
	if err := validateKind(kind); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entries, err := os.ReadDir(filepath.Join(r.root, kind))
	if err != nil {
		return nil, err
	}
	items := make([]Asset, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		item, readErr := r.read(kind, entry.Name())
		if errors.Is(readErr, fs.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		items = append(items, item)
	}
	sort.Slice(items, func(left int, right int) bool {
		if items[left].Source != items[right].Source {
			return items[left].Source < items[right].Source
		}
		return items[left].Name < items[right].Name
	})
	return items, nil
}

// Get returns one asset and its selected Markdown body.
func (r *Repository) Get(kind string, id string) (Asset, error) {
	if err := validateKind(kind); err != nil {
		return Asset{}, err
	}
	if err := validateID(id); err != nil {
		return Asset{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, err := r.read(kind, id)
	if errors.Is(err, fs.ErrNotExist) {
		return Asset{}, ErrNotFound
	}
	return item, err
}

// GetVersion returns immutable Markdown for an explicitly requested asset version.
// Metadata remains current, but CurrentVersion and Body describe the frozen task binding.
func (r *Repository) GetVersion(kind string, id string, version string) (Asset, error) {
	if err := validateKind(kind); err != nil {
		return Asset{}, err
	}
	if err := validateID(id); err != nil {
		return Asset{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, err := r.read(kind, id)
	if errors.Is(err, fs.ErrNotExist) {
		return Asset{}, ErrNotFound
	}
	if err != nil {
		return Asset{}, err
	}
	body, err := filedata.ReadText(r.versionPath(kind, id, version))
	if errors.Is(err, fs.ErrNotExist) {
		return Asset{}, ErrNotFound
	}
	if err != nil {
		return Asset{}, err
	}
	item.CurrentVersion = version
	item.Body = body
	return item, nil
}

// Create writes initial metadata and the first immutable Markdown version.
func (r *Repository) Create(kind string, input UpsertInput) (Asset, error) {
	if err := validateKind(kind); err != nil {
		return Asset{}, err
	}
	if err := validateID(input.ID); err != nil {
		return Asset{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	directory := filepath.Join(r.root, kind, input.ID)
	if _, err := os.Stat(directory); err == nil {
		return Asset{}, ErrConflict
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC()
	metadata := Metadata{
		SchemaVersion: 1, Revision: 1, ID: input.ID, Kind: kind,
		Name: input.Name, Description: input.Description, Avatar: input.Avatar, Source: input.Source,
		Role: input.Role, Enabled: enabled, CurrentVersion: input.Version,
		DefaultSkillIDs: input.DefaultSkillIDs, Config: input.Config,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := os.MkdirAll(filepath.Join(directory, "versions"), 0o755); err != nil {
		return Asset{}, err
	}
	if err := filedata.WriteText(filepath.Join(directory, "versions", input.Version+".md"), input.Body); err != nil {
		return Asset{}, err
	}
	if err := filedata.WriteJSON(filepath.Join(directory, "asset.json"), metadata); err != nil {
		return Asset{}, err
	}
	return r.read(kind, input.ID)
}

// Update changes mutable metadata while preserving every published version file.
func (r *Repository) Update(kind string, id string, input UpsertInput) (Asset, error) {
	if err := validateKind(kind); err != nil {
		return Asset{}, err
	}
	if err := validateID(id); err != nil {
		return Asset{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	metadata, err := r.readMetadata(kind, id)
	if errors.Is(err, fs.ErrNotExist) {
		return Asset{}, ErrNotFound
	}
	if err != nil {
		return Asset{}, err
	}
	metadata.Name = input.Name
	metadata.Description = input.Description
	metadata.Avatar = input.Avatar
	metadata.Role = input.Role
	metadata.DefaultSkillIDs = input.DefaultSkillIDs
	metadata.Config = input.Config
	if input.Enabled != nil {
		metadata.Enabled = *input.Enabled
	}
	if input.Version != "" {
		if _, err := os.Stat(r.versionPath(kind, id, input.Version)); err != nil {
			return Asset{}, fmt.Errorf("select version: %w", err)
		}
		metadata.CurrentVersion = input.Version
	}
	metadata.Revision++
	metadata.UpdatedAt = time.Now().UTC()
	if err := filedata.WriteJSON(r.metadataPath(kind, id), metadata); err != nil {
		return Asset{}, err
	}
	return r.read(kind, id)
}

// AddVersion creates an immutable Markdown version and optionally makes it current.
func (r *Repository) AddVersion(kind string, id string, input VersionInput) (Asset, error) {
	if err := validateKind(kind); err != nil {
		return Asset{}, err
	}
	if err := validateID(id); err != nil {
		return Asset{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	metadata, err := r.readMetadata(kind, id)
	if errors.Is(err, fs.ErrNotExist) {
		return Asset{}, ErrNotFound
	}
	if err != nil {
		return Asset{}, err
	}
	versionPath := r.versionPath(kind, id, input.Version)
	if _, err := os.Stat(versionPath); err == nil {
		return Asset{}, ErrConflict
	}
	if err := filedata.WriteText(versionPath, input.Body); err != nil {
		return Asset{}, err
	}
	if input.Select {
		metadata.CurrentVersion = input.Version
	}
	metadata.Revision++
	metadata.UpdatedAt = time.Now().UTC()
	if err := filedata.WriteJSON(r.metadataPath(kind, id), metadata); err != nil {
		return Asset{}, err
	}
	return r.read(kind, id)
}

// Delete removes a non-system asset directory after callers verify references.
func (r *Repository) Delete(kind string, id string) error {
	if err := validateKind(kind); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	metadata, err := r.readMetadata(kind, id)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if metadata.Source == "SYSTEM" {
		return errors.New("system assets cannot be deleted")
	}
	return os.RemoveAll(filepath.Join(r.root, kind, id))
}

// read combines metadata, version names, and the selected Markdown body.
func (r *Repository) read(kind string, id string) (Asset, error) {
	metadata, err := r.readMetadata(kind, id)
	if err != nil {
		return Asset{}, err
	}
	entries, err := os.ReadDir(filepath.Join(r.root, kind, id, "versions"))
	if err != nil {
		return Asset{}, err
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			versions = append(versions, strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	body, err := filedata.ReadText(r.versionPath(kind, id, metadata.CurrentVersion))
	if err != nil {
		return Asset{}, err
	}
	return Asset{
		SchemaVersion: metadata.SchemaVersion, Revision: metadata.Revision,
		ID: metadata.ID, Kind: metadata.Kind, Name: metadata.Name,
		Description: metadata.Description, Avatar: metadata.Avatar, Source: metadata.Source, Role: metadata.Role,
		Enabled: metadata.Enabled, CurrentVersion: metadata.CurrentVersion,
		Versions: versions, DefaultSkillIDs: metadata.DefaultSkillIDs,
		Config: metadata.Config, Body: body, CreatedAt: metadata.CreatedAt, UpdatedAt: metadata.UpdatedAt,
	}, nil
}

// readMetadata loads only the mutable asset.json document.
func (r *Repository) readMetadata(kind string, id string) (Metadata, error) {
	var metadata Metadata
	err := filedata.ReadJSON(r.metadataPath(kind, id), &metadata)
	return metadata, err
}

// metadataPath returns the controlled JSON path for one asset.
func (r *Repository) metadataPath(kind string, id string) string {
	return filepath.Join(r.root, kind, id, "asset.json")
}

// versionPath returns one immutable Markdown version path.
func (r *Repository) versionPath(kind string, id string, version string) string {
	return filepath.Join(r.root, kind, id, "versions", version+".md")
}

// validateKind restricts callers to the four known catalog roots.
func validateKind(kind string) error {
	switch kind {
	case KindAgent, KindSkill, KindRule, KindKnowledge:
		return nil
	default:
		return errors.New("invalid catalog kind")
	}
}

// validateID prevents asset identifiers from escaping their catalog directory.
func validateID(id string) error {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, "\\/") {
		return errors.New("invalid catalog id")
	}
	return nil
}
