// Package identity creates and validates gotomux logical object IDs.
package identity

import (
	"fmt"
	"github.com/google/uuid"
)

// New returns a canonical UUIDv4 for persistence and tmux user options.
func New() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate logical identity: %w", err)
	}
	return id.String(), nil
}

// Validate accepts canonical RFC4122 UUIDv4 values only.
func Validate(id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return fmt.Errorf("logical identity is not a canonical UUID")
	}
	if (parsed.Version() != 4 && parsed.Version() != 5) || parsed.Variant() != uuid.RFC4122 {
		return fmt.Errorf("logical identity must be UUIDv4 or UUIDv5")
	}
	return nil
}
