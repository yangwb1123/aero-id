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

func (s *CommentServer) LikeComment(ctx context.Context, req *aeropb.LikeCommentRequest) (*aeropb.LikeCommentResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	res, err := s.comments.Like(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.LikeCommentResponse{LikeCount: int32(res.LikeCount)}, nil
}

func (s *CommentServer) UnlikeComment(ctx context.Context, req *aeropb.UnlikeCommentRequest) (*aeropb.UnlikeCommentResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	res, err := s.comments.Unlike(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.UnlikeCommentResponse{LikeCount: int32(res.LikeCount)}, nil
}

func (s *CommentServer) ReportComment(ctx context.Context, req *aeropb.ReportCommentRequest) (*aeropb.ReportCommentResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	if err := s.comments.Report(ctx, id, req.Reason); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ReportCommentResponse{}, nil
}

func (s *CommentServer) HideComment(ctx context.Context, req *aeropb.HideCommentRequest) (*aeropb.HideCommentResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	if err := s.comments.Hide(ctx, id); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.HideCommentResponse{}, nil
}

func (s *CommentServer) UnhideComment(ctx context.Context, req *aeropb.UnhideCommentRequest) (*aeropb.UnhideCommentResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	if err := s.comments.Unhide(ctx, id); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.UnhideCommentResponse{}, nil
}

func (s *CommentServer) EditComment(ctx context.Context, req *aeropb.EditCommentRequest) (*aeropb.EditCommentResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	c, err := s.comments.Edit(ctx, id, req.Content)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.EditCommentResponse{Comment: commentToPB(c)}, nil
}

func (s *CommentServer) ListReports(ctx context.Context, req *aeropb.ListReportsRequest) (*aeropb.ListReportsResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	var status *domain.ReportStatus
	if req.Status != nil {
		s, ok := reportStatusByName[*req.Status]
		if !ok {
			return nil, grpcError(apperr.BadRequest("comment.invalid", "invalid status filter"))
		}
		status = &s
	}
	list, err := s.comments.ListReports(ctx, id, status, pageFromPB(req.Page))
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ListReportsResponse{
		Reports:    reportsToPB(list.Reports),
		Users:      usersToPB(list.Users),
		NextCursor: list.NextCursor,
	}, nil
}

func (s *CommentServer) ResolveReport(ctx context.Context, req *aeropb.ResolveReportRequest) (*aeropb.ResolveReportResponse, error) {
	id, err := idgen.Parse(req.ReportId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid report_id"))
	}
	status, ok := reportStatusByName[req.Status]
	if !ok {
		return nil, grpcError(apperr.BadRequest("comment.invalid", "status must be resolved or dismissed"))
	}
	if err := s.comments.ResolveReport(ctx, id, status); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ResolveReportResponse{}, nil
}

// reportStatusByName / reportStatusName mirror the HTTP transport's string<->enum
// mapping so both transports speak the same report-status vocabulary.
var reportStatusByName = map[string]domain.ReportStatus{
	"open":      domain.ReportStatusOpen,
	"resolved":  domain.ReportStatusResolved,
	"dismissed": domain.ReportStatusDismissed,
}

func reportStatusName(s domain.ReportStatus) string {
	switch s {
	case domain.ReportStatusResolved:
		return "resolved"
	case domain.ReportStatusDismissed:
		return "dismissed"
	default:
		return "open"
	}
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
		LikeCount:  int32(c.LikeCount),
		Status:     int32(c.Status),
		CreatedAt:  timestamppb.New(c.CreatedAt),
	}
	if c.ParentID != nil {
		pb.ParentId = c.ParentID.String()
	}
	if c.EditedAt != nil {
		pb.EditedAt = timestamppb.New(*c.EditedAt)
	}
	return pb
}

func reportToPB(r *domain.CommentReport) *aeropb.CommentReport {
	pb := &aeropb.CommentReport{
		Id:          r.ID.String(),
		CommentId:   r.CommentID.String(),
		ReporterUid: r.ReporterUID,
		Reason:      r.Reason,
		Status:      reportStatusName(r.Status),
		CreatedAt:   timestamppb.New(r.CreatedAt),
		ResolvedBy:  r.ResolvedBy,
	}
	if r.ResolvedAt != nil {
		pb.ResolvedAt = timestamppb.New(*r.ResolvedAt)
	}
	return pb
}

func reportsToPB(rs []*domain.CommentReport) []*aeropb.CommentReport {
	out := make([]*aeropb.CommentReport, 0, len(rs))
	for _, r := range rs {
		out = append(out, reportToPB(r))
	}
	return out
}

func commentsToPB(cs []*domain.Comment) []*aeropb.Comment {
	out := make([]*aeropb.Comment, 0, len(cs))
	for _, c := range cs {
		out = append(out, commentToPB(c))
	}
	return out
}
