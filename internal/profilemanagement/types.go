package profilemanagement

import (
	"re/internal/contextbuilder"
	"time"
)

type Profile struct {
	contextbuilder.ContextProfile
	ID        string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CreateInput struct {
	Name        string
	Description string
	Selector    contextbuilder.Selector
	Providers   contextbuilder.ProviderSpec
	Enabled     *bool
}

// Supplied Selector and Providers replace the entire object; nil preserves it.
type UpdateInput struct {
	Name        *string
	Description *string
	Selector    *contextbuilder.Selector
	Providers   *contextbuilder.ProviderSpec
	Enabled     *bool
}

type ListFilter struct {
	Enabled *bool
	Limit   int
	Offset  int
}

type ProfilePage struct {
	Items []Profile
	Total int64
}
