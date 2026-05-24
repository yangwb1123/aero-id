package idgen

import "github.com/google/uuid"

type ID = uuid.UUID

func NewID() ID {
	id, err := uuid.NewV7()
	if err != nil {
		// V7 only fails if the system clock is broken; panicking here is acceptable.
		panic("uuid v7: " + err.Error())
	}
	return id
}

func Parse(s string) (ID, error) {
	return uuid.Parse(s)
}
