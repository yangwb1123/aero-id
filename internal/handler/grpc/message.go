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
	"github.com/aero/aero-id/internal/sso"
)

type MessageServer struct {
	aeropb.UnimplementedMessageServiceServer
	messages service.MessageService
}

func NewMessageServer(messages service.MessageService) *MessageServer {
	return &MessageServer{messages: messages}
}

func (s *MessageServer) Register(srv *grpc.Server) { aeropb.RegisterMessageServiceServer(srv, s) }

func (s *MessageServer) SendMessage(ctx context.Context, req *aeropb.SendMessageRequest) (*aeropb.SendMessageResponse, error) {
	in := service.SendMessageInput{
		ToUID:   req.ToUid,
		Kind:    domain.MessageKind(req.Kind),
		Content: req.Content,
	}
	if req.FileId != "" {
		fid, err := idgen.Parse(req.FileId)
		if err != nil {
			return nil, grpcError(apperr.BadRequest("message.invalid", "invalid file_id"))
		}
		in.FileID = &fid
	}
	m, err := s.messages.Send(ctx, in)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.SendMessageResponse{Message: msgToPB(m)}, nil
}

func (s *MessageServer) GetMessage(ctx context.Context, req *aeropb.GetMessageRequest) (*aeropb.GetMessageResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	m, err := s.messages.Get(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.GetMessageResponse{Message: msgToPB(m)}, nil
}

func (s *MessageServer) Inbox(ctx context.Context, req *aeropb.InboxRequest) (*aeropb.InboxResponse, error) {
	list, err := s.messages.Inbox(ctx, pageFromPB(req.Page), req.UnreadOnly)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.InboxResponse{
		Messages:   msgsToPB(list.Messages),
		Users:      usersToPB(list.Users),
		NextCursor: list.NextCursor,
	}, nil
}

func (s *MessageServer) Conversation(ctx context.Context, req *aeropb.ConversationRequest) (*aeropb.ConversationResponse, error) {
	list, err := s.messages.Conversation(ctx, req.WithUid, pageFromPB(req.Page))
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ConversationResponse{
		Messages:   msgsToPB(list.Messages),
		Users:      usersToPB(list.Users),
		NextCursor: list.NextCursor,
	}, nil
}

func (s *MessageServer) MarkRead(ctx context.Context, req *aeropb.MarkReadRequest) (*aeropb.MarkReadResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.messages.MarkRead(ctx, id); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.MarkReadResponse{}, nil
}

func (s *MessageServer) UnreadCount(ctx context.Context, _ *aeropb.UnreadCountRequest) (*aeropb.UnreadCountResponse, error) {
	n, err := s.messages.UnreadCount(ctx)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.UnreadCountResponse{UnreadCount: n}, nil
}

func (s *MessageServer) EditMessage(ctx context.Context, req *aeropb.EditMessageRequest) (*aeropb.EditMessageResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	m, err := s.messages.Edit(ctx, id, req.Content)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.EditMessageResponse{Message: msgToPB(m)}, nil
}

func (s *MessageServer) DeleteMessage(ctx context.Context, req *aeropb.DeleteMessageRequest) (*aeropb.DeleteMessageResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.messages.Delete(ctx, id); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.DeleteMessageResponse{}, nil
}

func (s *MessageServer) MarkConversationRead(ctx context.Context, req *aeropb.MarkConversationReadRequest) (*aeropb.MarkConversationReadResponse, error) {
	if err := s.messages.MarkConversationRead(ctx, req.WithUid); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.MarkConversationReadResponse{}, nil
}

func (s *MessageServer) ReactToMessage(ctx context.Context, req *aeropb.ReactToMessageRequest) (*aeropb.ReactToMessageResponse, error) {
	id, err := idgen.Parse(req.MessageId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid message_id"))
	}
	actor, ok := service.ActorFrom(ctx)
	if !ok {
		return nil, grpcError(apperr.Unauthorized("auth.unauthorized", "missing authenticated actor"))
	}
	if err := s.messages.ReactToMessage(ctx, id, actor); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ReactToMessageResponse{}, nil
}

func (s *MessageServer) UnreactMessage(ctx context.Context, req *aeropb.UnreactMessageRequest) (*aeropb.UnreactMessageResponse, error) {
	id, err := idgen.Parse(req.MessageId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid message_id"))
	}
	actor, ok := service.ActorFrom(ctx)
	if !ok {
		return nil, grpcError(apperr.Unauthorized("auth.unauthorized", "missing authenticated actor"))
	}
	if err := s.messages.UnreactMessage(ctx, id, actor); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.UnreactMessageResponse{}, nil
}

func msgToPB(m *domain.Message) *aeropb.Message {
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
		out = append(out, msgToPB(m))
	}
	return out
}

// usersToPB is shared with the comment handler.
func usersToPB(users map[string]sso.User) map[string]*aeropb.User {
	out := make(map[string]*aeropb.User, len(users))
	for uid, u := range users {
		out[uid] = &aeropb.User{Uid: u.UID, Nickname: u.Nickname, Avatar: u.Avatar}
	}
	return out
}
