package domain

import "time"

type File struct {
	ID          ID
	OwnerUID    string
	Name        string
	Size        int64
	MimeType    string
	StorageKey  string
	StorageType string
	Checksum    string
	Visibility  Visibility
	Meta        map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
