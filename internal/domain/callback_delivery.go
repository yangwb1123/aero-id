package domain

import "time"

// CallbackDeliveryMetadata is the operational read model, not the signed body,
// remote response, subscription endpoint/secret or a worker execution grant.
// It represents the current local row; there is no attempt-history guarantee.
type CallbackDeliveryMetadata struct {
	ID              ID
	SubscriptionID  ID
	EventID         ID
	TenantID        string
	Action          string
	Status          string
	Attempt         int32
	Revision        int64
	LeaseGeneration int64
	LeaseUntil      *time.Time
	NextAttemptAt   time.Time
	StatusCode      int32
	ErrorCode       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CallbackDeliveryFilter struct {
	Status string
	Page   Page
}
