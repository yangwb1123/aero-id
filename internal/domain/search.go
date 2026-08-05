package domain

// SearchKind identifies which entity type a search hit came from.
type SearchKind string

const (
	SearchKindMessages SearchKind = "messages"
	SearchKindComments SearchKind = "comments"
	SearchKindFiles    SearchKind = "files"
)

// SearchHit is a union type: exactly one of Message, Comment, or File is
// non-nil, matching the Kind field. Snippet holds a ts_headline-highlighted
// excerpt of the matched content (with <mark>...</mark> tags) when available.
type SearchHit struct {
	Kind    SearchKind
	Message *Message // non-nil when Kind == SearchKindMessages
	Comment *Comment // non-nil when Kind == SearchKindComments
	File    *File    // non-nil when Kind == SearchKindFiles
	Snippet string   // highlighted excerpt; empty when not available (e.g. file search)
}
