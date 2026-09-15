package profilemanagement

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"re/internal/contextbuilder"
)

const MaxProfileBytes = 1 << 20

type Validator struct{}

func (Validator) Validate(p Profile) error {
	if strings.TrimSpace(p.Name) == "" {
		return Invalid("name", "must not be empty")
	}
	if !utf8.ValidString(p.Name) || !utf8.ValidString(p.Description) {
		return Invalid("profile", "must be valid UTF-8")
	}
	data, err := json.Marshal(p.ContextProfile)
	if err != nil {
		return Invalid("profile", err.Error())
	}
	if len(data) > MaxProfileBytes {
		return Invalid("profile", "must not exceed 1 MiB encoded JSON")
	}
	// PostgreSQL JSONB cannot store NUL, including in nested keys and values.
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return Invalid("profile", err.Error())
	}
	if hasNUL(value) {
		return Invalid("profile", "must not contain NUL")
	}
	if err := p.ContextProfile.Validate(); err != nil {
		var definition *contextbuilder.DefinitionError
		if errors.As(err, &definition) {
			return Invalid(definition.Field, definition.Detail)
		}
		return Invalid("profile", err.Error())
	}
	return nil
}

func hasNUL(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.ContainsRune(v, 0)
	case []any:
		for _, item := range v {
			if hasNUL(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range v {
			if strings.ContainsRune(key, 0) || hasNUL(item) {
				return true
			}
		}
	}
	return false
}

func validateID(id string) error {
	if err := uuid.Validate(id); err != nil || len(id) != 36 {
		return Invalid("id", "must be a UUID")
	}
	return nil
}
