package configset

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Service validates and serializes configuration-set mutations.
type Service struct {
	// repository owns the only persistent configuration-set index.
	repository *Repository
	// mu prevents concurrent create/update requests from losing revisions.
	mu sync.Mutex
}

// NewService creates a configuration-set application service.
func NewService(repository *Repository) *Service {
	return &Service{repository: repository}
}

// List returns configuration sets ordered by their persisted display order.
func (s *Service) List() ([]Set, error) {
	return s.repository.Load()
}

// Get resolves one immutable set ID.
func (s *Service) Get(id string) (Set, error) {
	sets, err := s.repository.Load()
	if err != nil {
		return Set{}, err
	}
	for _, set := range sets {
		if set.ID == id {
			return set, nil
		}
	}
	return Set{}, errors.New("configuration set not found")
}

// Resolve returns selected sets in plan order and fails on stale references.
func (s *Service) Resolve(ids []string) ([]Set, error) {
	resolved := make([]Set, 0, len(ids))
	for _, id := range ids {
		set, err := s.Get(id)
		if err != nil {
			return nil, fmt.Errorf("configuration set %s is unavailable: %w", id, err)
		}
		resolved = append(resolved, set)
	}
	return resolved, nil
}

// Create validates content, assigns stable IDs, and appends one new set.
func (s *Service) Create(input UpsertInput) (Set, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := normalizeInput(&input); err != nil {
		return Set{}, err
	}
	sets, err := s.repository.Load()
	if err != nil {
		return Set{}, err
	}
	now := time.Now().UTC()
	set := Set{
		SchemaVersion: 1,
		Revision:      1,
		ID:            fmt.Sprintf("cfg_%d", now.UnixNano()),
		Name:          input.Name,
		Description:   input.Description,
		Entries:       input.Entries,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	sets = append(sets, set)
	if err := s.repository.Save(sets); err != nil {
		return Set{}, err
	}
	return set, nil
}

// Update replaces user-editable fields while preserving identity and creation time.
func (s *Service) Update(id string, input UpsertInput) (Set, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := normalizeInput(&input); err != nil {
		return Set{}, err
	}
	sets, err := s.repository.Load()
	if err != nil {
		return Set{}, err
	}
	for index := range sets {
		if sets[index].ID != id {
			continue
		}
		sets[index].Name = input.Name
		sets[index].Description = input.Description
		sets[index].Entries = input.Entries
		sets[index].Revision++
		sets[index].UpdatedAt = time.Now().UTC()
		if err := s.repository.Save(sets); err != nil {
			return Set{}, err
		}
		return sets[index], nil
	}
	return Set{}, errors.New("configuration set not found")
}

// Delete removes one unreferenced set; task-reference validation is performed by
// the HTTP/task layer before this catalog mutation is allowed.
func (s *Service) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sets, err := s.repository.Load()
	if err != nil {
		return err
	}
	next := make([]Set, 0, len(sets))
	found := false
	for _, set := range sets {
		if set.ID == id {
			found = true
			continue
		}
		next = append(next, set)
	}
	if !found {
		return errors.New("configuration set not found")
	}
	return s.repository.Save(next)
}

// normalizeInput trims metadata, validates unique entry names, and assigns entry IDs.
func normalizeInput(input *UpsertInput) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" {
		return errors.New("configuration set name is required")
	}
	if len(input.Entries) == 0 {
		return errors.New("at least one configuration entry is required")
	}
	seenNames := map[string]bool{}
	for index := range input.Entries {
		entry := &input.Entries[index]
		entry.Name = strings.TrimSpace(entry.Name)
		entry.Content = strings.TrimSpace(entry.Content)
		if entry.Name == "" || entry.Content == "" {
			return errors.New("configuration entry name and content are required")
		}
		normalizedName := strings.ToLower(entry.Name)
		if seenNames[normalizedName] {
			return errors.New("configuration entry names must be unique within a set")
		}
		seenNames[normalizedName] = true
		if strings.TrimSpace(entry.ID) == "" {
			entry.ID = fmt.Sprintf("entry_%d_%d", time.Now().UTC().UnixNano(), index)
		}
	}
	return nil
}
