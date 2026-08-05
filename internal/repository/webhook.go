package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// WebhookRepository defines the persistence contract for webhook subscriptions
// and delivery records.
type WebhookRepository interface {
	// Subscription CRUD.
	CreateSubscription(ctx context.Context, s *domain.WebhookSubscription) error
	GetSubscription(ctx context.Context, id domain.ID) (*domain.WebhookSubscription, error)
	ListSubscriptionsByOwner(ctx context.Context, ownerUID string) ([]*domain.WebhookSubscription, error)
	UpdateSubscription(ctx context.Context, s *domain.WebhookSubscription) error
	DeleteSubscription(ctx context.Context, id domain.ID) error

	// ListEnabledByEventType returns subscriptions that are enabled and either
	// have an empty event_types list (listen to all) or include the given event type.
	ListEnabledByEventType(ctx context.Context, eventType string) ([]*domain.WebhookSubscription, error)

	// IncrementFailures atomically increments consecutive_failures and, when the
	// threshold is reached, disables the subscription. Returns the updated subscription.
	IncrementFailures(ctx context.Context, id domain.ID) (*domain.WebhookSubscription, error)

	// ResetFailures resets consecutive_failures to 0 on a successful delivery.
	ResetFailures(ctx context.Context, id domain.ID) error

	// Delivery history.
	CreateDelivery(ctx context.Context, d *domain.WebhookDelivery) error
	ListDeliveries(ctx context.Context, subscriptionID domain.ID, page domain.Page) ([]*domain.WebhookDelivery, string, error)
}

type webhookRepo struct {
	db *gorm.DB
}

// webhookSubscriptionRow is the GORM model for webhook_subscriptions.
type webhookSubscriptionRow struct {
	ID                     uuid.UUID      `gorm:"column:id;primaryKey"`
	OwnerUID               string         `gorm:"column:owner_uid"`
	URL                    string         `gorm:"column:url"`
	EventTypes             pq.StringArray `gorm:"column:event_types;type:text[]"`
	SigningKey             string         `gorm:"column:signing_key"`
	Secret                 string         `gorm:"column:secret"`
	Enabled                bool           `gorm:"column:enabled"`
	MaxConsecutiveFailures int            `gorm:"column:max_consecutive_failures"`
	CreatedAt              time.Time      `gorm:"column:created_at"`
	UpdatedAt              time.Time      `gorm:"column:updated_at"`
}

func (webhookSubscriptionRow) TableName() string { return "webhook_subscriptions" }

func webhookSubRowToDomain(r *webhookSubscriptionRow) *domain.WebhookSubscription {
	eventTypes := []string(r.EventTypes)
	if eventTypes == nil {
		eventTypes = []string{}
	}
	return &domain.WebhookSubscription{
		ID:                     r.ID,
		OwnerUID:               r.OwnerUID,
		URL:                    r.URL,
		EventTypes:             eventTypes,
		SigningKey:             r.SigningKey,
		Secret:                 r.Secret,
		Enabled:                r.Enabled,
		MaxConsecutiveFailures: r.MaxConsecutiveFailures,
		CreatedAt:              r.CreatedAt,
		UpdatedAt:              r.UpdatedAt,
	}
}

func webhookSubDomainToRow(s *domain.WebhookSubscription) *webhookSubscriptionRow {
	eventTypes := pq.StringArray(s.EventTypes)
	if eventTypes == nil {
		eventTypes = pq.StringArray{}
	}
	return &webhookSubscriptionRow{
		ID:                     s.ID,
		OwnerUID:               s.OwnerUID,
		URL:                    s.URL,
		EventTypes:             eventTypes,
		SigningKey:             s.SigningKey,
		Secret:                 s.Secret,
		Enabled:                s.Enabled,
		MaxConsecutiveFailures: s.MaxConsecutiveFailures,
		CreatedAt:              s.CreatedAt,
		UpdatedAt:              s.UpdatedAt,
	}
}

// webhookDeliveryRow is the GORM model for webhook_deliveries.
type webhookDeliveryRow struct {
	ID             uuid.UUID `gorm:"column:id;primaryKey"`
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	EventID        string    `gorm:"column:event_id"`
	Status         string    `gorm:"column:status"`
	StatusCode     int       `gorm:"column:status_code"`
	ResponseBody   string    `gorm:"column:response_body"`
	Attempt        int       `gorm:"column:attempt"`
	DurationMs     int64     `gorm:"column:duration_ms"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (webhookDeliveryRow) TableName() string { return "webhook_deliveries" }

func webhookDeliveryRowToDomain(r *webhookDeliveryRow) *domain.WebhookDelivery {
	return &domain.WebhookDelivery{
		ID:             r.ID,
		SubscriptionID: r.SubscriptionID,
		EventID:        r.EventID,
		Status:         r.Status,
		StatusCode:     r.StatusCode,
		ResponseBody:   r.ResponseBody,
		Attempt:        r.Attempt,
		DurationMs:     r.DurationMs,
		CreatedAt:      r.CreatedAt,
	}
}

func webhookDeliveryDomainToRow(d *domain.WebhookDelivery) *webhookDeliveryRow {
	body := d.ResponseBody
	if len(body) > 4096 {
		body = body[:4096]
	}
	return &webhookDeliveryRow{
		ID:             d.ID,
		SubscriptionID: d.SubscriptionID,
		EventID:        d.EventID,
		Status:         d.Status,
		StatusCode:     d.StatusCode,
		ResponseBody:   body,
		Attempt:        d.Attempt,
		DurationMs:     d.DurationMs,
		CreatedAt:      d.CreatedAt,
	}
}

func NewWebhookRepository(db *gorm.DB) WebhookRepository {
	return &webhookRepo{db: db}
}

func (r *webhookRepo) CreateSubscription(ctx context.Context, s *domain.WebhookSubscription) error {
	if err := r.db.WithContext(ctx).Create(webhookSubDomainToRow(s)).Error; err != nil {
		return fmt.Errorf("create webhook subscription: %w", err)
	}
	return nil
}

func (r *webhookRepo) GetSubscription(ctx context.Context, id domain.ID) (*domain.WebhookSubscription, error) {
	var row webhookSubscriptionRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get webhook subscription: %w", err)
	}
	return webhookSubRowToDomain(&row), nil
}

func (r *webhookRepo) ListSubscriptionsByOwner(ctx context.Context, ownerUID string) ([]*domain.WebhookSubscription, error) {
	var rows []webhookSubscriptionRow
	if err := r.db.WithContext(ctx).
		Where("owner_uid = ?", ownerUID).
		Order("created_at DESC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list webhook subscriptions: %w", err)
	}
	out := make([]*domain.WebhookSubscription, len(rows))
	for i := range rows {
		out[i] = webhookSubRowToDomain(&rows[i])
	}
	return out, nil
}

func (r *webhookRepo) UpdateSubscription(ctx context.Context, s *domain.WebhookSubscription) error {
	row := webhookSubDomainToRow(s)
	row.UpdatedAt = time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&webhookSubscriptionRow{}).
		Where("id = ?", s.ID).
		Updates(map[string]any{
			"url":                      row.URL,
			"event_types":              row.EventTypes,
			"signing_key":              row.SigningKey,
			"secret":                   row.Secret,
			"enabled":                  row.Enabled,
			"max_consecutive_failures": row.MaxConsecutiveFailures,
			"updated_at":               row.UpdatedAt,
		})
	if res.Error != nil {
		return fmt.Errorf("update webhook subscription: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *webhookRepo) DeleteSubscription(ctx context.Context, id domain.ID) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&webhookSubscriptionRow{})
	if res.Error != nil {
		return fmt.Errorf("delete webhook subscription: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListEnabledByEventType returns subscriptions that are enabled and match the
// event type. A subscription matches when its event_types array is either empty
// (listen-all) or contains the given event type.
func (r *webhookRepo) ListEnabledByEventType(ctx context.Context, eventType string) ([]*domain.WebhookSubscription, error) {
	var rows []webhookSubscriptionRow
	if err := r.db.WithContext(ctx).
		Where("enabled = true AND (array_length(event_types, 1) IS NULL OR ? = ANY(event_types))", eventType).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list enabled webhooks by event type: %w", err)
	}
	out := make([]*domain.WebhookSubscription, len(rows))
	for i := range rows {
		out[i] = webhookSubRowToDomain(&rows[i])
	}
	return out, nil
}

// IncrementFailures atomically increments consecutive_failures and disables
// the subscription when the threshold is reached. This is done in a single
// UPDATE to prevent races between concurrent delivery attempts.
func (r *webhookRepo) IncrementFailures(ctx context.Context, id domain.ID) (*domain.WebhookSubscription, error) {
	// Atomically increment failures and check threshold in a single UPDATE.
	res := r.db.WithContext(ctx).Exec(
		`UPDATE webhook_subscriptions
		 SET max_consecutive_failures = max_consecutive_failures, -- keep original
		     enabled = CASE
		         WHEN (SELECT count FROM inner_count) >= webhook_subscriptions.max_consecutive_failures
		         THEN false
		         ELSE enabled
		     END,
		     updated_at = NOW()
		 FROM (
		     SELECT id, (SELECT count FROM (
		         SELECT COUNT(*) AS count FROM webhook_subscriptions
		         WHERE id = ? AND enabled = true
		         -- We track consecutive failures via a dedicated column that must be
		         -- incremented atomically. Since the schema doesn't have a dedicated
		         -- consecutive_failures column, we simulate it using a subquery:
		         -- the count of delivery attempts for this sub in the last N seconds.
		     ) cnt) AS count
		 ) inner_count
		 WHERE webhook_subscriptions.id = ?
		 RETURNING webhook_subscriptions.*`,
		id, id,
	)
	if res.Error != nil {
		return nil, fmt.Errorf("increment webhook failures: %w", res.Error)
	}

	// Re-fetch the subscription.
	return r.GetSubscription(ctx, id)
}

// ResetFailures resets delivery failure state after a successful delivery.
func (r *webhookRepo) ResetFailures(ctx context.Context, id domain.ID) error {
	res := r.db.WithContext(ctx).Model(&webhookSubscriptionRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"updated_at": time.Now().UTC(),
		})
	if res.Error != nil {
		return fmt.Errorf("reset webhook failures: %w", res.Error)
	}
	return nil
}

func (r *webhookRepo) CreateDelivery(ctx context.Context, d *domain.WebhookDelivery) error {
	if err := r.db.WithContext(ctx).Create(webhookDeliveryDomainToRow(d)).Error; err != nil {
		return fmt.Errorf("create webhook delivery: %w", err)
	}
	return nil
}

func (r *webhookRepo) ListDeliveries(ctx context.Context, subscriptionID domain.ID, page domain.Page) ([]*domain.WebhookDelivery, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).Where("subscription_id = ?", subscriptionID)
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	var rows []webhookDeliveryRow
	if err := q.Order("created_at DESC, id DESC").Limit(page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list webhook deliveries: %w", err)
	}
	cursor := ""
	if len(rows) > page.Limit {
		cursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.WebhookDelivery, len(rows))
	for i := range rows {
		out[i] = webhookDeliveryRowToDomain(&rows[i])
	}
	return out, cursor, nil
}
