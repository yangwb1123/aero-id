package domain

import "errors"

var ErrNotFound = errors.New("domain: not found")

var ErrDuplicate = errors.New("domain: duplicate")
