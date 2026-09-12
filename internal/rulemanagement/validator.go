package rulemanagement

import (
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"re/internal/ruleengine"
)

const MaxContentBytes = 1 << 20

type Validator struct{}

// Validate compiles in isolation; it does not execute GRL or change runtime caches.
func (Validator) Validate(rule Rule) error {
	if strings.TrimSpace(rule.Name) == "" {
		return Invalid("name", "must not be empty")
	}
	if strings.TrimSpace(rule.Content) == "" {
		return Invalid("rule_content", "must not be empty")
	}
	if len(rule.Content) > MaxContentBytes {
		return Invalid("rule_content", "must not exceed 1 MiB")
	}
	for field, value := range map[string]string{"name": rule.Name, "description": rule.Description, "rule_content": rule.Content} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return Invalid(field, "must be valid UTF-8 without NUL")
		}
	}
	if rule.Salience < -2147483648 || rule.Salience > 2147483647 {
		return Invalid("salience", "must be a 32-bit integer")
	}
	if err := ruleengine.ValidateContent(rule.Content); err != nil {
		return Invalid("rule_content", err.Error())
	}
	return nil
}

func validateID(id string) error {
	if err := uuid.Validate(id); err != nil || len(id) != 36 {
		return Invalid("id", "must be a UUID")
	}
	return nil
}
