package domain

// SummaryApplicationRequest is the redacted, source-scoped projection batch
// produced by a connector. The repository applies the complete batch in one
// local transaction; it must not make a remote call while applying it.
// SummaryCoverage declares which projection classes a source revision covers.
// Dataset coverage is per snapshot dataset. Sources and Memberships cover the
// complete source-reference or membership set for the revision; they are not
// inferred from a requested snapshot dataset when Explicit is true.
type SummaryCoverage struct {
	Datasets    []AccountDataset
	Sources     bool
	Memberships bool
	// Explicit distinguishes a source contract that deliberately does not cover
	// a class from an old connector using the pre-coverage contract. New
	// connectors should always set this to true.
	Explicit bool
}

// SummaryApplyOutcome is the durable decision made by the local projection
// fence. The values are intentionally strings because they are also written to
// operation and sync-job result documents.
type SummaryApplyOutcome string

const (
	SummaryApplied       SummaryApplyOutcome = "applied"
	SummaryStaleIgnored  SummaryApplyOutcome = "stale_ignored"
	SummaryReconciled    SummaryApplyOutcome = "reconciled"
	SummaryIncomplete    SummaryApplyOutcome = "incomplete"
)

// SummaryApplicationResult describes the durable decision made by the local
// projection fence. Applied includes recording a newer observed fence even
// when the summary was incomplete and therefore changed no projection row.
// The boolean fields are retained as convenient predicates for internal
// callers; Outcome is the authoritative typed value.
type SummaryApplicationResult struct {
	Outcome      SummaryApplyOutcome
	Applied      bool
	StaleIgnored bool
	Reconciled   bool
}

type SummaryApplicationRequest struct {
	AccountID         ID
	Source            string
	// Version is a source revision, globally monotonic for the tuple
	// (account_id, source, coverage). A source region change must not reset the
	// revision. SourceRegion identifies the authority that produced the
	// revision and is part of equal-version conflict detection.
	SourceRegion      string
	Version           int64
	RequestedDatasets []AccountDataset
	Coverage          SummaryCoverage
	Complete          bool
	SkippedDatasets   []string
	Reconcile         bool

	Snapshots   []*AccountSnapshot
	Sources     []*AccountSource
	Memberships []*AccountMembership
}
