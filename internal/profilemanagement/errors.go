package profilemanagement

import (
	"errors"
	"fmt"
)

var (
	ErrInvalid  = errors.New("invalid profile request")
	ErrNotFound = errors.New("profile not found")
	ErrConflict = errors.New("profile name already exists or profile was modified concurrently")
)

func Invalid(field, message string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalid, field, message)
}
