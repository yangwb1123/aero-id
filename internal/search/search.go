package search

import (
	"context"

	"github.com/aero/aero-id/internal/comment"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/file"
	"github.com/aero/aero-id/internal/message"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

const (
	defaultLimit = 20
	maxLimit     = 50
	minLimit     = 1
)

type SearchHit = domain.SearchHit

type Service interface {
	Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error)
}

type searchService struct {
	messages message.MessageRepository
	comments comment.Repository
	files    file.Repository
}

func NewService(
	msgs message.MessageRepository,
	comments comment.Repository,
	files file.Repository,
) Service {
	return &searchService{messages: msgs, comments: comments, files: files}
}

func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return nil, apperr.BadRequest("search.invalid", "query is required")
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit < minLimit {
		limit = minLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	if len(kinds) == 0 {
		kinds = []domain.SearchKind{domain.SearchKindMessages, domain.SearchKindComments, domain.SearchKindFiles}
	}

	var hits []SearchHit
	for _, kind := range kinds {
		var err error
		hits, err = s.searchKind(ctx, actor, query, limit, kind, hits)
		if err != nil {
			return nil, err
		}
	}
	return hits, nil
}

func (s *searchService) searchKind(ctx context.Context, actor, query string, limit int, kind domain.SearchKind, hits []SearchHit) ([]SearchHit, error) {
	switch kind {
	case domain.SearchKindMessages:
		msgs, err := s.messages.SearchMessages(ctx, actor, query, limit)
		if err != nil {
			return nil, apperr.Internal("search.failed", "failed to search messages").Wrap(err)
		}
		for _, m := range msgs {
			hits = append(hits, SearchHit{Kind: domain.SearchKindMessages, Message: m})
		}
	case domain.SearchKindComments:
		cmts, err := s.comments.SearchComments(ctx, actor, query, limit)
		if err != nil {
			return nil, apperr.Internal("search.failed", "failed to search comments").Wrap(err)
		}
		for _, c := range cmts {
			hits = append(hits, SearchHit{Kind: domain.SearchKindComments, Comment: c})
		}
	case domain.SearchKindFiles:
		files, err := s.files.SearchFiles(ctx, actor, query, limit)
		if err != nil {
			return nil, apperr.Internal("search.failed", "failed to search files").Wrap(err)
		}
		for _, f := range files {
			hits = append(hits, SearchHit{Kind: domain.SearchKindFiles, File: f})
		}
	}
	return hits, nil
}
