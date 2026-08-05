package comment

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/sso"
)

type CommentServer struct {
	aeropb.UnimplementedCommentServiceServer
	svc Service
}

func NewCommentServer(svc Service) *CommentServer {
	return &CommentServer{svc: svc}
}

func (s *CommentServer) Register(srv *grpc.Server) { aeropb.RegisterCommentServiceServer(srv, s) }

func (s *CommentServer) CreateComment(ctx context.Context, req *aeropb.CreateCommentRequest) (*aeropb.CreateCommentResponse, error) {
	tid, err := idgen.Parse(req.TargetId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("comment.invalid", "invalid target_id"))
	}
	in := CreateCommentInput{
		TargetType: domain.TargetType(req.TargetType),
		TargetID:   tid,
		Content:    req.Content,
	}
	if req.ParentId != "" {
		pid, err := idgen.Parse(req.ParentId)
		if err != nil {
			return nil, apperr.GRPCError(apperr.BadRequest("comment.invalid", "invalid parent_id"))
		}
		in.ParentID = &pid
	}
	c, err := s.svc.Create(ctx, in)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.CreateCommentResponse{Comment: CommentToPB(c)}, nil
}

func (s *CommentServer) ListComments(ctx context.Context, req *aeropb.ListCommentsRequest) (*aeropb.ListCommentsResponse, error) {
	tid, err := idgen.Parse(req.TargetId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("comment.invalid", "invalid target_id"))
	}
	list, err := s.svc.List(ctx, domain.TargetType(req.TargetType), tid, pageFromPB(req.Page))
	if err != nil {
		return nil, apperr.GRPCError(err)
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
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid root_id"))
	}
	list, err := s.svc.Replies(ctx, rootID, pageFromPB(req.Page))
	if err != nil {
		return nil, apperr.GRPCError(err)
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
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.svc.Delete(ctx, id); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.DeleteCommentResponse{}, nil
}

func (s *CommentServer) LikeComment(ctx context.Context, req *aeropb.LikeCommentRequest) (*aeropb.LikeCommentResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	res, err := s.svc.Like(ctx, id)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.LikeCommentResponse{LikeCount: int32(res.LikeCount)}, nil //nolint:gosec // like count fits int32
}

func (s *CommentServer) UnlikeComment(ctx context.Context, req *aeropb.UnlikeCommentRequest) (*aeropb.UnlikeCommentResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	res, err := s.svc.Unlike(ctx, id)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.UnlikeCommentResponse{LikeCount: int32(res.LikeCount)}, nil //nolint:gosec // like count fits int32
}

func (s *CommentServer) ReportComment(ctx context.Context, req *aeropb.ReportCommentRequest) (*aeropb.ReportCommentResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	if err := s.svc.Report(ctx, id, req.Reason); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ReportCommentResponse{}, nil
}

func (s *CommentServer) HideComment(ctx context.Context, req *aeropb.HideCommentRequest) (*aeropb.HideCommentResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	if err := s.svc.Hide(ctx, id); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.HideCommentResponse{}, nil
}

func (s *CommentServer) UnhideComment(ctx context.Context, req *aeropb.UnhideCommentRequest) (*aeropb.UnhideCommentResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	if err := s.svc.Unhide(ctx, id); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.UnhideCommentResponse{}, nil
}

func (s *CommentServer) EditComment(ctx context.Context, req *aeropb.EditCommentRequest) (*aeropb.EditCommentResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	c, err := s.svc.Edit(ctx, id, req.Content)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.EditCommentResponse{Comment: CommentToPB(c)}, nil
}

func (s *CommentServer) ListReports(ctx context.Context, req *aeropb.ListReportsRequest) (*aeropb.ListReportsResponse, error) {
	id, err := idgen.Parse(req.CommentId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid comment_id"))
	}
	var status *domain.ReportStatus
	if req.Status != nil {
		s, ok := reportStatusByName[*req.Status]
		if !ok {
			return nil, apperr.GRPCError(apperr.BadRequest("comment.invalid", "invalid status filter"))
		}
		status = &s
	}
	list, err := s.svc.ListReports(ctx, id, status, pageFromPB(req.Page))
	if err != nil {
		return nil, apperr.GRPCError(err)
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
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid report_id"))
	}
	status, ok := reportStatusByName[req.Status]
	if !ok {
		return nil, apperr.GRPCError(apperr.BadRequest("comment.invalid", "status must be resolved or dismissed"))
	}
	if err := s.svc.ResolveReport(ctx, id, status); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ResolveReportResponse{}, nil
}

func CommentToPB(c *Comment) *aeropb.Comment {
	pb := &aeropb.Comment{
		Id:         c.ID.String(),
		OwnerUid:   c.OwnerUID,
		TargetType: string(c.TargetType),
		TargetId:   c.TargetID.String(),
		RootId:     c.RootID.String(),
		Content:    c.Content,
		ReplyCount: int32(c.ReplyCount), //nolint:gosec
		LikeCount:  int32(c.LikeCount),  //nolint:gosec
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

func reportToPB(r *CommentReport) *aeropb.CommentReport {
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

func reportsToPB(rs []*CommentReport) []*aeropb.CommentReport {
	out := make([]*aeropb.CommentReport, 0, len(rs))
	for _, r := range rs {
		out = append(out, reportToPB(r))
	}
	return out
}

func commentsToPB(cs []*Comment) []*aeropb.Comment {
	out := make([]*aeropb.Comment, 0, len(cs))
	for _, c := range cs {
		out = append(out, CommentToPB(c))
	}
	return out
}

func usersToPB(users map[string]sso.User) map[string]*aeropb.User {
	out := make(map[string]*aeropb.User, len(users))
	for k, u := range users {
		out[k] = &aeropb.User{
			Uid:      u.UID,
			Nickname: u.Nickname,
			Avatar:   u.Avatar,
		}
	}
	return out
}

func pageFromPB(p *aeropb.Page) domain.Page {
	if p == nil {
		return domain.Page{}
	}
	return domain.Page{Limit: int(p.Limit), Cursor: p.Cursor}
}
