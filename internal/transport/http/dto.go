package http

import (
	"re/internal/rulemanagement"
	"time"
)

type ruleRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Content     *string `json:"rule_content"`
	Salience    *int    `json:"salience"`
	Enabled     *bool   `json:"enabled"`
}

type ruleResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Content     string    `json:"rule_content"`
	Salience    int       `json:"salience"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func response(r rulemanagement.Rule) ruleResponse {
	return ruleResponse{ID: r.ID, Name: r.Name, Description: r.Description, Content: r.Content, Salience: r.Salience, Enabled: r.Enabled, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}
