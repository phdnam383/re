package profilemanagement

import (
	"context"
	"re/internal/contextbuilder"
	"strings"
)

type Service struct {
	repo      Repository
	validator Validator
}

func NewService(repo Repository) *Service { return &Service{repo: repo} }

func (s *Service) Create(ctx context.Context, in CreateInput) (Profile, error) {
	profile := Profile{ContextProfile: contextbuilder.ContextProfile{Name: strings.TrimSpace(in.Name), Description: in.Description, Selector: in.Selector, Providers: in.Providers}, Enabled: true}
	if in.Enabled != nil {
		profile.Enabled = *in.Enabled
	}
	if err := s.validator.Validate(profile); err != nil {
		return Profile{}, err
	}
	return s.repo.Create(ctx, profile)
}

func (s *Service) Get(ctx context.Context, id string) (Profile, error) {
	if err := validateID(id); err != nil {
		return Profile{}, err
	}
	return s.repo.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, filter ListFilter) (ProfilePage, error) {
	if filter.Limit == 0 {
		filter.Limit = 20
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return ProfilePage{}, Invalid("limit", "must be between 1 and 100")
	}
	if filter.Offset < 0 {
		return ProfilePage{}, Invalid("offset", "must not be negative")
	}
	return s.repo.List(ctx, filter)
}

func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (Profile, error) {
	if in.Name == nil && in.Description == nil && in.Selector == nil && in.Providers == nil && in.Enabled == nil {
		return Profile{}, Invalid("body", "must contain at least one update field")
	}
	profile, err := s.Get(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	expected := profile.UpdatedAt
	if in.Name != nil {
		profile.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		profile.Description = *in.Description
	}
	if in.Selector != nil {
		profile.Selector = *in.Selector
	}
	if in.Providers != nil {
		profile.Providers = *in.Providers
	}
	if in.Enabled != nil {
		profile.Enabled = *in.Enabled
	}
	if err := s.validator.Validate(profile); err != nil {
		return Profile{}, err
	}
	return s.repo.Update(ctx, profile, expected)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	return s.repo.Delete(ctx, id)
}
