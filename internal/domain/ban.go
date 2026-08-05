package domain

import "time"

// UserBan records that uid has been banned (directionally, from actor_uid's
// action). A nil expires_at means the ban is permanent; Expired() checks
// the wall clock against expires_at.
type UserBan struct {
	UID       string
	ActorUID  string
	Reason    string
	ExpiresAt *time.Time
	CreatedAt time.Time
}

// Expired reports whether the ban has passed its expiry window. A permanent
// ban (ExpiresAt == nil) never expires.
func (b *UserBan) Expired() bool {
	return b.ExpiresAt != nil && time.Now().UTC().After(*b.ExpiresAt)
}
