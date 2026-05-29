package domain

import "time"

type Message struct {
	ID        ID
	FromUID   string
	ToUID     string
	Kind      MessageKind
	Content   string
	FileID    *ID
	ReadAt    *time.Time
	EditedAt  *time.Time
	Meta      map[string]any
	CreatedAt time.Time
}
