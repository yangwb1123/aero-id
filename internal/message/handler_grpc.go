package message

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

type GRPCHandler struct {
	aeropb.UnimplementedMessageServiceServer
	svc Service
}

func NewGRPCHandler(svc Service) *GRPCHandler {
	return &GRPCHandler{svc: svc}
}

func (s *GRPCHandler) Register(srv *grpc.Server) {
	aeropb.RegisterMessageServiceServer(srv, s)
}

func (s *GRPCHandler) SendMessage(ctx context.Context, req *aeropb.SendMessageRequest) (*aeropb.SendMessageResponse, error) {
	if req.Kind < 0 || req.Kind > int32(domain.MessageKindSystem) {
		return nil, apperr.GRPCError(apperr.BadRequest("message.invalid_kind", "invalid message kind"))
	}
	in := SendMessageInput{
		ToUID:   req.ToUid,
		Kind:    domain.MessageKind(req.Kind),
		Content: req.Content,
	}
	if req.FileId != "" {
		fid, err := idgen.Parse(req.FileId)
		if err != nil {
			return nil, apperr.GRPCError(apperr.BadRequest("message.invalid", "invalid file_id"))
		}
		in.FileID = &fid
	}
	m, err := s.svc.Send(ctx, in)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.SendMessageResponse{Message: MsgToPB(m)}, nil
}

func (s *GRPCHandler) GetMessage(ctx context.Context, req *aeropb.GetMessageRequest) (*aeropb.GetMessageResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	m, err := s.svc.Get(ctx, id)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.GetMessageResponse{Message: MsgToPB(m)}, nil
}

func (s *GRPCHandler) Inbox(ctx context.Context, req *aeropb.InboxRequest) (*aeropb.InboxResponse, error) {
	list, err := s.svc.Inbox(ctx, pageFromPB(req.Page), req.UnreadOnly)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.InboxResponse{
		Messages:   msgsToPB(list.Messages),
		Users:      usersToPB(list.Users),
		NextCursor: list.NextCursor,
	}, nil
}

func (s *GRPCHandler) Conversation(ctx context.Context, req *aeropb.ConversationRequest) (*aeropb.ConversationResponse, error) {
	list, err := s.svc.Conversation(ctx, req.WithUid, pageFromPB(req.Page))
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ConversationResponse{
		Messages:   msgsToPB(list.Messages),
		Users:      usersToPB(list.Users),
		NextCursor: list.NextCursor,
	}, nil
}

func (s *GRPCHandler) MarkRead(ctx context.Context, req *aeropb.MarkReadRequest) (*aeropb.MarkReadResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.svc.MarkRead(ctx, id); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.MarkReadResponse{}, nil
}

func (s *GRPCHandler) UnreadCount(ctx context.Context, _ *aeropb.UnreadCountRequest) (*aeropb.UnreadCountResponse, error) {
	n, err := s.svc.UnreadCount(ctx)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.UnreadCountResponse{UnreadCount: n}, nil
}

func (s *GRPCHandler) EditMessage(ctx context.Context, req *aeropb.EditMessageRequest) (*aeropb.EditMessageResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	m, err := s.svc.Edit(ctx, id, req.Content)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.EditMessageResponse{Message: MsgToPB(m)}, nil
}

func (s *GRPCHandler) DeleteMessage(ctx context.Context, req *aeropb.DeleteMessageRequest) (*aeropb.DeleteMessageResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.svc.Delete(ctx, id); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.DeleteMessageResponse{}, nil
}

func (s *GRPCHandler) MarkConversationRead(ctx context.Context, req *aeropb.MarkConversationReadRequest) (*aeropb.MarkConversationReadResponse, error) {
	if err := s.svc.MarkConversationRead(ctx, req.WithUid); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.MarkConversationReadResponse{}, nil
}

func (s *GRPCHandler) ReactToMessage(ctx context.Context, req *aeropb.ReactToMessageRequest) (*aeropb.ReactToMessageResponse, error) {
	id, err := idgen.Parse(req.MessageId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid message_id"))
	}
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, apperr.GRPCError(apperr.Unauthorized("auth.unauthorized", "missing authenticated actor"))
	}
	if err := s.svc.ReactToMessage(ctx, id, actor); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ReactToMessageResponse{}, nil
}

func (s *GRPCHandler) UnreactMessage(ctx context.Context, req *aeropb.UnreactMessageRequest) (*aeropb.UnreactMessageResponse, error) {
	id, err := idgen.Parse(req.MessageId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid message_id"))
	}
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, apperr.GRPCError(apperr.Unauthorized("auth.unauthorized", "missing authenticated actor"))
	}
	if err := s.svc.UnreactMessage(ctx, id, actor); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.UnreactMessageResponse{}, nil
}

func pageFromPB(p *aeropb.Page) domain.Page {
	if p == nil {
		return domain.Page{}
	}
	return domain.Page{Limit: int(p.Limit), Cursor: p.Cursor}
}

func MsgToPB(m *domain.Message) *aeropb.Message {
	pb := &aeropb.Message{
		Id:        m.ID.String(),
		FromUid:   m.FromUID,
		ToUid:     m.ToUID,
		Kind:      int32(m.Kind),
		Content:   m.Content,
		CreatedAt: timestamppb.New(m.CreatedAt),
	}
	if m.FileID != nil {
		pb.FileId = m.FileID.String()
	}
	if m.ReadAt != nil {
		pb.ReadAt = timestamppb.New(*m.ReadAt)
	}
	if m.EditedAt != nil {
		pb.EditedAt = timestamppb.New(*m.EditedAt)
	}
	return pb
}

func msgsToPB(ms []*domain.Message) []*aeropb.Message {
	out := make([]*aeropb.Message, 0, len(ms))
	for _, m := range ms {
		out = append(out, MsgToPB(m))
	}
	return out
}

func usersToPB(users map[string]sso.User) map[string]*aeropb.User {
	out := make(map[string]*aeropb.User, len(users))
	for uid, u := range users {
		out[uid] = &aeropb.User{Uid: u.UID, Nickname: u.Nickname, Avatar: u.Avatar}
	}
	return out
}
