package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type CommentServer struct {
	aeropb.UnimplementedCommentServiceServer
	comments service.CommentService
}

func NewCommentServer(comments service.CommentService) *CommentServer {
	return &CommentServer{comments: comments}
}

func (s *CommentServer) Register(srv *grpc.Server) { aeropb.RegisterCommentServiceServer(srv, s) }

func (s *CommentServer) CreateComment(ctx context.Context, req *aeropb.CreateCommentRequest) (*aeropb.CreateCommentResponse, error) {
	tid, err := idgen.Parse(req.TargetId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("comment.invalid", "invalid target_id"))
	}
	in := service.CreateCommentInput{
		TargetType: domain.TargetType(req.TargetType),
		TargetID:   tid,
		Content:    req.Content,
	}
	if req.ParentId != "" {
		pid, err := idgen.Parse(req.ParentId)
		if err != nil {
			return nil, grpcError(apperr.BadRequest("comment.invalid", "invalid parent_id"))
		}
		in.ParentID = &pid
	}
	c, err := s.comments.Create(ctx, in)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.CreateCommentResponse{Comment: commentToPB(c)}, nil
}

func (s *CommentServer) ListComments(ctx context.Context, req *aeropb.ListCommentsRequest) (*aeropb.ListCommentsResponse, error) {
	tid, err := idgen.Parse(req.TargetId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("comment.invalid", "invalid target_id"))
	}
	list, err := s.comments.List(ctx, domain.TargetType(req.TargetType), tid, pageFromPB(req.Page))
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ListCommentsResponse{
		Comments:   commentsToPB(list.Comments),
		Users:      usersToPB(list.Users),
		NextCursor: list.NextCursor,
	}, nil
}

func (s *CommentServer) Replies(ctx context.Context, req *aeropb.RepliesRequest) (*aeropb.RepliesResponse, error) {
	rootID, err := idgen.Parse(req.RootId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid root_id"))
	}
	list, err := s.comments.Replies(ctx, rootID, pageFromPB(req.Page))
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.RepliesResponse{
		Comments:   commentsToPB(list.Comments),
		Users:      usersToPB(list.Users),
		NextCursor: list.NextCursor,
	}, nil
}

func (s *CommentServer) DeleteComment(ctx context.Context, req *aeropb.DeleteCommentRequest) (*aeropb.DeleteCommentResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.comments.Delete(ctx, id); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.DeleteCommentResponse{}, nil
}

func commentToPB(c *domain.Comment) *aeropb.Comment {
	pb := &aeropb.Comment{
		Id:         c.ID.String(),
		OwnerUid:   c.OwnerUID,
		TargetType: string(c.TargetType),
		TargetId:   c.TargetID.String(),
		RootId:     c.RootID.String(),
		Content:    c.Content,
		ReplyCount: int32(c.ReplyCount),
		Status:     int32(c.Status),
		CreatedAt:  timestamppb.New(c.CreatedAt),
	}
	if c.ParentID != nil {
		pb.ParentId = c.ParentID.String()
	}
	return pb
}

func commentsToPB(cs []*domain.Comment) []*aeropb.Comment {
	out := make([]*aeropb.Comment, 0, len(cs))
	for _, c := range cs {
		out = append(out, commentToPB(c))
	}
	return out
}
