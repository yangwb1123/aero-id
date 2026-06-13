package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

const (
	searchDefaultLimit = 20
	searchMaxLimit     = 50
	searchMinLimit     = 1
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

func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]domain.SearchHit, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return nil, apperr.BadRequest("search.invalid", "query is required")
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

	var hits []domain.SearchHit
	for _, kind := range kinds {
		switch kind {
		case domain.SearchKindMessages:
			msgs, err := s.messages.SearchMessages(ctx, actor, query, limit)
			if err != nil {
				return nil, apperr.Internal("search.failed", "failed to search messages").Wrap(err)
			}
			for _, m := range msgs {
				m := m
				hits = append(hits, domain.SearchHit{Kind: domain.SearchKindMessages, Message: m})
			}
		case domain.SearchKindComments:
			cmts, err := s.comments.SearchComments(ctx, actor, query, limit)
			if err != nil {
				return nil, apperr.Internal("search.failed", "failed to search comments").Wrap(err)
			}
			for _, c := range cmts {
				c := c
				hits = append(hits, domain.SearchHit{Kind: domain.SearchKindComments, Comment: c})
			}
		case domain.SearchKindFiles:
			files, err := s.files.SearchFiles(ctx, actor, query, limit)
			if err != nil {
				return nil, apperr.Internal("search.failed", "failed to search files").Wrap(err)
			}
			for _, f := range files {
				f := f
				hits = append(hits, domain.SearchHit{Kind: domain.SearchKindFiles, File: f})
			}
		}
	}
	return hits, nil
}
