package domain

import "time"

type CallbackSubscription struct {
	ID               ID
	OwnerUID         string
	TenantID         string
	Endpoint         string
	EventTypes       []string
	SecretCiphertext string
	Enabled          bool
	Verified         bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type CallbackDelivery struct {
	ID               ID
	SubscriptionID   ID
	EventID          ID
	TenantID         string
	Action           string
	Payload          map[string]any
	Status           string
	Attempt          int
	LeaseOwner       string
	LeaseUntil       *time.Time
	NextAttemptAt    time.Time
	StatusCode       int
	ResponseBody     string
	ErrorCode        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Endpoint         string
	SecretCiphertext string
	OwnerUID         string
}
