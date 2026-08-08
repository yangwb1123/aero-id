package repository

import "reflect"

// FullAuditLedgerRepository is implemented by the durable ledger repo: the
// read/verify surface plus the in-transaction append. NewAuditLedgerRepository
// returns this type so wiring the ledger into account/operation/receipt/sync-job
// repositories is checked at compile time (fail-closed wiring).
type FullAuditLedgerRepository interface {
	AuditLedgerRepository
	AuditLedgerTransactionalRepository
}

// requireAuditLedger fails closed at wiring time. A nil or typed-nil ledger
// would silently disable the transactional audit path for account, operation,
// event-receipt, and sync-job mutations; a nil dependency is a programmer
// error and must stop the process at startup (the fail-closed position for a
// compliance ledger). The typed-nil check covers every nil-able kind (pointer
// today, but also map/slice/chan/func/interface implementations), not just
// pointers, so no implementation shape can smuggle a nil ledger past wiring.
func requireAuditLedger(name string, audit AuditLedgerTransactionalRepository) AuditLedgerTransactionalRepository {
	if audit == nil {
		panic("repository: " + name + " requires a non-nil AuditLedgerTransactionalRepository")
	}
	v := reflect.ValueOf(audit)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		if v.IsNil() {
			panic("repository: " + name + " requires a non-nil AuditLedgerTransactionalRepository")
		}
	}
	return audit
}
