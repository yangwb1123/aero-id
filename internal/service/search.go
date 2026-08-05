package service

import (
	"context"
	"time"
	"unicode"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

const (
	searchDefaultLimit = 20
	searchMaxLimit     = 50
	searchMinLimit     = 1
	// searchMaxQueryLen caps the raw query string to prevent abusive long
	// inputs from locking a database connection with complex tsquery parsing.
	searchMaxQueryLen = 200
	// searchTimeout is the maximum wall-clock time a single search kind query
	// (messages, comments, or files) is allowed to take. The context deadline
	// is applied per-kind, so a three-kind search may take up to 3× this long.
	searchTimeout = 5 * time.Second
)

// SearchService provides cross-entity full-text search.
type SearchService interface {
	Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]domain.SearchHit, error)
}

type searchService struct {
	messages repository.MessageRepository
	comments repository.CommentRepository
	files    repository.FileRepository
}

// NewSearchService constructs a SearchService backed by the three entity repos.
func NewSearchService(
	msgs repository.MessageRepository,
	comments repository.CommentRepository,
	files repository.FileRepository,
) SearchService {
	return &searchService{messages: msgs, comments: comments, files: files}
}

//nolint:cyclop,funlen // legacy search fans out across allow-listed indexes and merges their results.
func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]domain.SearchHit, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return nil, apperr.BadRequest("search.invalid", "query is required")
	}
	if len([]rune(query)) > searchMaxQueryLen {
		return nil, apperr.BadRequest("search.too_long", "query too long")
	}
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	if limit < searchMinLimit {
		limit = searchMinLimit
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}

	// Determine which kinds to search; empty means all three.
	if len(kinds) == 0 {
		kinds = []domain.SearchKind{domain.SearchKindMessages, domain.SearchKindComments, domain.SearchKindFiles}
	}

	// CJK fallback: PostgreSQL's `simple` tsvector dictionary does not tokenise
	// Chinese/Japanese/Korean text, so plainto_tsquery('simple', ?) matches
	// nothing. When the query contains CJK characters we try the FTS path first
	// (it may still match via latinised content), and if it returns zero results
	// we fall back to ILIKE '%query%'.
	needsCJK := containsCJK(query)

	var hits []domain.SearchHit
	for _, kind := range kinds {
		searchCtx, cancel := context.WithTimeout(ctx, searchTimeout)
		var err error
		switch kind {
		case domain.SearchKindMessages:
			// Try FTS with snippet first.
			var msgResults []repository.MessageSearchResult
			msgResults, err = s.messages.SearchMessagesWithSnippet(searchCtx, actor, query, limit)
			if err == nil {
				if len(msgResults) == 0 && needsCJK {
					var msgs []*domain.Message
					msgs, err = s.messages.SearchMessagesLike(searchCtx, actor, query, limit)
					for _, m := range msgs {
						m := m
						hits = append(hits, domain.SearchHit{
							Kind:    domain.SearchKindMessages,
							Message: m,
							// No snippet for LIKE fallback.
						})
					}
				} else {
					for _, r := range msgResults {
						r := r
						hits = append(hits, domain.SearchHit{
							Kind:    domain.SearchKindMessages,
							Message: r.Message,
							Snippet: r.Snippet,
						})
					}
				}
			}
		case domain.SearchKindComments:
			var cmtResults []repository.CommentSearchResult
			cmtResults, err = s.comments.SearchCommentsWithSnippet(searchCtx, actor, query, limit)
			if err == nil {
				if len(cmtResults) == 0 && needsCJK {
					var cmts []*domain.Comment
					cmts, err = s.comments.SearchCommentsLike(searchCtx, actor, query, limit)
					for _, c := range cmts {
						c := c
						hits = append(hits, domain.SearchHit{
							Kind:    domain.SearchKindComments,
							Comment: c,
						})
					}
				} else {
					for _, r := range cmtResults {
						r := r
						hits = append(hits, domain.SearchHit{
							Kind:    domain.SearchKindComments,
							Comment: r.Comment,
							Snippet: r.Snippet,
						})
					}
				}
			}
		case domain.SearchKindFiles:
			var files []*domain.File
			files, err = s.files.SearchFiles(searchCtx, actor, query, limit)
			if err == nil {
				if len(files) == 0 && needsCJK {
					files, err = s.files.SearchFilesLike(searchCtx, actor, query, limit)
				}
				for _, f := range files {
					f := f
					hits = append(hits, domain.SearchHit{Kind: domain.SearchKindFiles, File: f})
				}
			}
		}
		cancel()
		if err != nil {
			return nil, apperr.Internal("search.failed", "failed to search "+string(kind)).Wrap(err)
		}
	}
	return hits, nil
}

// containsCJK reports whether query contains any CJK (Chinese/Japanese/Korean)
// character. PostgreSQL's 'simple' tsdict does not tokenise these characters,
// so FTS-based search returns nothing; the caller should fall back to ILIKE.
func containsCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}
