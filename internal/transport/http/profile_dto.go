package http

import (
	"re/internal/contextbuilder"
	"re/internal/profilemanagement"
	"time"
)

type profileRequest struct {
	Name        *string                      `json:"name"`
	Description *string                      `json:"description"`
	Selector    *contextbuilder.Selector     `json:"selector"`
	Providers   *contextbuilder.ProviderSpec `json:"providers"`
	Enabled     *bool                        `json:"enabled"`
}

type profileResponse struct {
	ID          string                      `json:"id"`
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Selector    contextbuilder.Selector     `json:"selector"`
	Providers   contextbuilder.ProviderSpec `json:"providers"`
	Enabled     bool                        `json:"enabled"`
	CreatedAt   time.Time                   `json:"created_at"`
	UpdatedAt   time.Time                   `json:"updated_at"`
}

func profileResponseFrom(p profilemanagement.Profile) profileResponse {
	return profileResponse{ID: p.ID, Name: p.Name, Description: p.Description, Selector: p.Selector, Providers: p.Providers, Enabled: p.Enabled, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}
