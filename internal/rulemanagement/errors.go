package rulemanagement

import (
	"errors"
	"fmt"
)

var (
	ErrInvalid  = errors.New("invalid rule request")
	ErrNotFound = errors.New("rule not found")
	ErrConflict = errors.New("rule name already exists or rule was modified concurrently")
)

func Invalid(field, message string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalid, field, message)
}
