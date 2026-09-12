package rulemanagement

import (
	"context"
	"re/internal/analysis"
	"strings"
)

type Service struct {
	repo      Repository
	validator Validator
}

func NewService(repo Repository) *Service { return &Service{repo: repo} }

func (s *Service) Create(ctx context.Context, in CreateInput) (Rule, error) {
	rule := Rule{RuleDefinition: analysis.RuleDefinition{Name: strings.TrimSpace(in.Name), Description: in.Description, Content: in.Content, Salience: in.Salience}, Enabled: true}
	if in.Enabled != nil {
		rule.Enabled = *in.Enabled
	}
	if err := s.validator.Validate(rule); err != nil {
		return Rule{}, err
	}
	return s.repo.Create(ctx, rule)
}

func (s *Service) Get(ctx context.Context, id string) (Rule, error) {
	if err := validateID(id); err != nil {
		return Rule{}, err
	}
	return s.repo.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) (RulePage, error) {
	if filter.Limit == 0 {
		filter.Limit = 20
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return RulePage{}, Invalid("limit", "must be between 1 and 100")
	}
	if filter.Offset < 0 {
		return RulePage{}, Invalid("offset", "must not be negative")
	}
	return s.repo.List(ctx, filter)
}

func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (Rule, error) {
	if in.Name == nil && in.Description == nil && in.Content == nil && in.Salience == nil && in.Enabled == nil {
		return Rule{}, Invalid("body", "must contain at least one update field")
	}
	rule, err := s.Get(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	expected := rule.UpdatedAt
	if in.Name != nil {
		rule.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		rule.Description = *in.Description
	}
	if in.Content != nil {
		rule.Content = *in.Content
	}
	if in.Salience != nil {
		rule.Salience = *in.Salience
	}
	if in.Enabled != nil {
		rule.Enabled = *in.Enabled
	}
	if err := s.validator.Validate(rule); err != nil {
		return Rule{}, err
	}
	return s.repo.Update(ctx, rule, expected)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	return s.repo.Delete(ctx, id)
}
