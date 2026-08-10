package domain

import "gorm.io/gorm"

// Rule (b) positive fixture: internal/domain/ non-test files may import only
// stdlib + internal/pkg/* (+ the exact-string google/uuid exception, EX-3).
// A third-party import such as gorm.io/gorm must trip the rule.
