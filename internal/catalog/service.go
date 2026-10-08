package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`)
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

// Service validates catalog changes and exposes role-based lookups to task planning.
type Service struct {
	// repository owns the file mapping and immutable version files.
	repository *Repository
}

// NewService creates the catalog application service.
func NewService(repository *Repository) *Service {
	return &Service{repository: repository}
}

// List returns one asset kind.
func (s *Service) List(kind string) ([]Asset, error) {
	return s.repository.List(kind)
}

// Get returns one selected asset version.
func (s *Service) Get(kind string, id string) (Asset, error) {
	return s.repository.Get(kind, id)
}

// GetVersion returns one immutable version without changing the globally selected version.
func (s *Service) GetVersion(kind string, id string, version string) (Asset, error) {
	version = strings.TrimSpace(version)
	if !versionPattern.MatchString(version) {
		return Asset{}, errors.New("invalid semantic version")
	}
	return s.repository.GetVersion(kind, id, version)
}

// FindAgentByRole returns the enabled system or user agent selected for a task role.
func (s *Service) FindAgentByRole(role string) (Asset, error) {
	items, err := s.repository.List(KindAgent)
	if err != nil {
		return Asset{}, err
	}
	for _, item := range items {
		if item.Enabled && item.Role == role {
			return item, nil
		}
	}
	return Asset{}, ErrNotFound
}

// Create validates the initial version and creates an asset.
func (s *Service) Create(kind string, input UpsertInput) (Asset, error) {
	if err := validateUpsert(kind, &input, true); err != nil {
		return Asset{}, err
	}
	return s.repository.Create(kind, input)
}

// Update validates mutable fields and updates an existing asset.
func (s *Service) Update(kind string, id string, input UpsertInput) (Asset, error) {
	input.ID = id
	if err := validateUpsert(kind, &input, false); err != nil {
		return Asset{}, err
	}
	return s.repository.Update(kind, id, input)
}

// AddVersion publishes a new immutable version.
func (s *Service) AddVersion(kind string, id string, input VersionInput) (Asset, error) {
	input.Version = strings.TrimSpace(input.Version)
	if !versionPattern.MatchString(input.Version) {
		return Asset{}, errors.New("version must use semantic version format such as 1.1.0")
	}
	if strings.TrimSpace(input.Body) == "" {
		return Asset{}, errors.New("version body is required")
	}
	return s.repository.AddVersion(kind, id, input)
}

// Delete removes a user or AI asset.
func (s *Service) Delete(kind string, id string) error {
	return s.repository.Delete(kind, id)
}

// validateUpsert normalizes and validates asset fields before file writes.
func validateUpsert(kind string, input *UpsertInput, creating bool) error {
	input.ID = strings.TrimSpace(strings.ToLower(input.ID))
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Avatar = strings.TrimSpace(input.Avatar)
	input.Role = strings.TrimSpace(input.Role)
	input.Version = strings.TrimSpace(input.Version)
	if creating && !idPattern.MatchString(input.ID) {
		return errors.New("id must contain 2-64 lowercase letters, numbers, or hyphens")
	}
	if input.Name == "" {
		return errors.New("name is required")
	}
	if kind == KindKnowledge {
		if err := validateKnowledgeConfig(input); err != nil {
			return err
		}
	}
	if creating {
		if input.Source == "" {
			input.Source = "USER"
		}
		input.Source = strings.ToUpper(strings.TrimSpace(input.Source))
		if input.Source != "SYSTEM" && input.Source != "USER" && input.Source != "AI" {
			return errors.New("source must be SYSTEM, USER, or AI")
		}
		if !versionPattern.MatchString(input.Version) {
			return errors.New("version must use semantic version format such as 1.0.0")
		}
		if kind != KindKnowledge && strings.TrimSpace(input.Body) == "" {
			return errors.New("Markdown body is required")
		}
	}
	return nil
}

// validateKnowledgeConfig normalizes usable local-directory and MCP references.
// Local paths are resolved before persistence so later task snapshots do not depend
// on the server process working directory or an unresolved symbolic link.
func validateKnowledgeConfig(input *UpsertInput) error {
	if input.Config == nil {
		input.Config = map[string]any{}
	}
	configType, _ := input.Config["type"].(string)
	configType = strings.ToUpper(strings.TrimSpace(configType))
	if configType == "" {
		configType = "LOCAL_DIRECTORY"
	}
	switch configType {
	case "LOCAL_DIRECTORY":
		pathValue, _ := input.Config["path"].(string)
		pathValue = strings.TrimSpace(pathValue)
		if pathValue == "" {
			return errors.New("local knowledge directory path is required")
		}
		absolutePath, err := filepath.Abs(pathValue)
		if err != nil {
			return errors.New("local knowledge directory path is invalid")
		}
		resolvedPath, err := filepath.EvalSymlinks(absolutePath)
		if err != nil {
			return errors.New("local knowledge directory does not exist")
		}
		info, err := os.Stat(resolvedPath)
		if err != nil || !info.IsDir() {
			return errors.New("local knowledge path must be an existing directory")
		}
		input.Config = map[string]any{"type": configType, "path": filepath.Clean(resolvedPath)}
	case "MCP":
		server, _ := input.Config["server"].(string)
		server = strings.TrimSpace(server)
		if server == "" {
			return errors.New("MCP knowledge server name is required")
		}
		input.Config = map[string]any{"type": configType, "server": server}
	default:
		return errors.New("knowledge type must be LOCAL_DIRECTORY or MCP")
	}
	return nil
}
