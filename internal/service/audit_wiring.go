package service

// AuditLoggerWired reports whether a non-nil AuditLogger is configured on the
// service. The composition root asserts this for every service that guards
// audit writes with `if s.audit != nil` (the 10 fail-closed Log sites), so
// those skip guards are provably unreachable in production: a composition
// root that forgets an audit option fails startup instead of silently running
// fail-open. Unit tests may still build these services without an auditor —
// the nil guards are kept for exactly that construction path.
func (s *fileService) AuditLoggerWired() bool    { return s.audit != nil }
func (s *messageService) AuditLoggerWired() bool { return s.audit != nil }
func (s *commentService) AuditLoggerWired() bool { return s.audit != nil }
func (s *blockService) AuditLoggerWired() bool   { return s.audit != nil }
func (s *adminService) AuditLoggerWired() bool   { return s.audit != nil }
func (s *banService) AuditLoggerWired() bool     { return s.audit != nil }

// The hold and cutover services guard their fact append with the same
// `if s.audit != nil` skip guard (0 = disabled convention for unit tests);
// the composition root asserts them so a forgotten audit service fails
// startup instead of letting hold/cutover mutations commit without their
// durable facts.
func (s *auditHoldService) AuditLoggerWired() bool     { return s.audit != nil }
func (s *legacyCutoverService) AuditLoggerWired() bool { return s.audit != nil }
