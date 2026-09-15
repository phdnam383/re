package profilemanagement

import (
	"context"
	"time"
)

type Repository interface {
	Create(context.Context, Profile) (Profile, error)
	Get(context.Context, string) (Profile, error)
	List(context.Context, ListFilter) (ProfilePage, error)
	// Update must reject a write if the stored timestamp differs from expected.
	Update(ctx context.Context, profile Profile, expected time.Time) (Profile, error)
	Delete(context.Context, string) error
}
