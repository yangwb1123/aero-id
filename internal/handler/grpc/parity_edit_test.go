package grpc_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/aero/aero-id/internal/domain"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
)

// ---- comment edit / moderation ----

type editCommentSvc struct {
	service.CommentService
	editFn    func(context.Context, domain.ID, string) (*domain.Comment, error)
	listFn    func(context.Context, domain.ID, *domain.ReportStatus, domain.Page) (*service.ReportList, error)
	resolveFn func(context.Context, domain.ID, domain.ReportStatus) error
}

func (c editCommentSvc) Edit(ctx context.Context, id domain.ID, content string) (*domain.Comment, error) {
	return c.editFn(ctx, id, content)
}
func (c editCommentSvc) ListReports(ctx context.Context, id domain.ID, st *domain.ReportStatus, p domain.Page) (*service.ReportList, error) {
	return c.listFn(ctx, id, st, p)
}
func (c editCommentSvc) ResolveReport(ctx context.Context, id domain.ID, st domain.ReportStatus) error {
	return c.resolveFn(ctx, id, st)
}

func TestGRPC_EditComment_Success(t *testing.T) {
	edited := time.Now().UTC()
	svc := editCommentSvc{editFn: func(_ context.Context, id domain.ID, content string) (*domain.Comment, error) {
		return &domain.Comment{ID: id, OwnerUID: "u1", Content: content, EditedAt: &edited}, nil
	}}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	resp, err := client.EditComment(authCtx(), &aeropb.EditCommentRequest{Id: idgen.NewID().String(), Content: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Comment.Content != "new" {
		t.Fatalf("content = %q", resp.Comment.Content)
	}
	if resp.Comment.EditedAt == nil {
		t.Fatal("edited_at not propagated")
	}
}

func TestGRPC_EditComment_NonAuthorPermissionDenied(t *testing.T) {
	svc := editCommentSvc{editFn: func(context.Context, domain.ID, string) (*domain.Comment, error) {
		return nil, apperr.Forbidden("comment.forbidden", "not author")
	}}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	_, err := client.EditComment(authCtx(), &aeropb.EditCommentRequest{Id: idgen.NewID().String(), Content: "x"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
}

func TestGRPC_EditComment_InvalidID(t *testing.T) {
	conn := dial(t, grpcsrv.NewCommentServer(editCommentSvc{}))
	client := aeropb.NewCommentServiceClient(conn)
	_, err := client.EditComment(authCtx(), &aeropb.EditCommentRequest{Id: "nope", Content: "x"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPC_ListReports_Shape(t *testing.T) {
	cid := idgen.NewID()
	resolved := time.Now().UTC()
	var gotStatus *domain.ReportStatus
	svc := editCommentSvc{listFn: func(_ context.Context, _ domain.ID, st *domain.ReportStatus, _ domain.Page) (*service.ReportList, error) {
		gotStatus = st
		return &service.ReportList{
			Reports: []*domain.CommentReport{{
				ID: idgen.NewID(), CommentID: cid, ReporterUID: "u2", Reason: "spam",
				Status: domain.ReportStatusResolved, ResolvedAt: &resolved, ResolvedBy: "u1",
			}},
			Users:      map[string]sso.User{"u2": {UID: "u2", Nickname: "Bob"}},
			NextCursor: "nc",
		}, nil
	}}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	filter := "resolved"
	resp, err := client.ListReports(authCtx(), &aeropb.ListReportsRequest{CommentId: cid.String(), Status: &filter})
	if err != nil {
		t.Fatal(err)
	}
	if gotStatus == nil || *gotStatus != domain.ReportStatusResolved {
		t.Fatalf("status filter not forwarded: %v", gotStatus)
	}
	if len(resp.Reports) != 1 || resp.Reports[0].Status != "resolved" || resp.Reports[0].Reason != "spam" {
		t.Fatalf("reports = %+v", resp.Reports)
	}
	if resp.Reports[0].ResolvedAt == nil || resp.Reports[0].ResolvedBy != "u1" {
		t.Fatalf("resolved fields = %+v", resp.Reports[0])
	}
	if resp.NextCursor != "nc" {
		t.Fatalf("next_cursor = %q", resp.NextCursor)
	}
	if u, ok := resp.Users["u2"]; !ok || u.Nickname != "Bob" {
		t.Fatalf("users = %+v", resp.Users)
	}
}

func TestGRPC_ResolveReport_Success(t *testing.T) {
	var gotStatus domain.ReportStatus
	svc := editCommentSvc{resolveFn: func(_ context.Context, _ domain.ID, st domain.ReportStatus) error {
		gotStatus = st
		return nil
	}}
	conn := dial(t, grpcsrv.NewCommentServer(svc))
	client := aeropb.NewCommentServiceClient(conn)
	if _, err := client.ResolveReport(authCtx(), &aeropb.ResolveReportRequest{ReportId: idgen.NewID().String(), Status: "dismissed"}); err != nil {
		t.Fatal(err)
	}
	if gotStatus != domain.ReportStatusDismissed {
		t.Fatalf("status = %v, want dismissed", gotStatus)
	}
}

func TestGRPC_ResolveReport_InvalidStatus(t *testing.T) {
	conn := dial(t, grpcsrv.NewCommentServer(editCommentSvc{}))
	client := aeropb.NewCommentServiceClient(conn)
	_, err := client.ResolveReport(authCtx(), &aeropb.ResolveReportRequest{ReportId: idgen.NewID().String(), Status: "bogus"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// ---- message edit / delete / conversation read ----

type editMsgSvc struct {
	service.MessageService
	editFn     func(context.Context, domain.ID, string) (*domain.Message, error)
	deleteFn   func(context.Context, domain.ID) error
	convReadFn func(context.Context, string) error
}

func (m editMsgSvc) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
	return m.editFn(ctx, id, content)
}
func (m editMsgSvc) Delete(ctx context.Context, id domain.ID) error { return m.deleteFn(ctx, id) }
func (m editMsgSvc) MarkConversationRead(ctx context.Context, with string) error {
	return m.convReadFn(ctx, with)
}

func TestGRPC_EditMessage_ReturnsEditedAt(t *testing.T) {
	edited := time.Now().UTC()
	svc := editMsgSvc{editFn: func(_ context.Context, id domain.ID, content string) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "u1", ToUID: "b", Content: content, EditedAt: &edited}, nil
	}}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)
	resp, err := client.EditMessage(authCtx(), &aeropb.EditMessageRequest{Id: idgen.NewID().String(), Content: "fixed"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "fixed" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
	if resp.Message.EditedAt == nil {
		t.Fatal("edited_at not propagated")
	}
}

func TestGRPC_EditMessage_NonSenderPermissionDenied(t *testing.T) {
	svc := editMsgSvc{editFn: func(context.Context, domain.ID, string) (*domain.Message, error) {
		return nil, apperr.Forbidden("message.forbidden", "not sender")
	}}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)
	_, err := client.EditMessage(authCtx(), &aeropb.EditMessageRequest{Id: idgen.NewID().String(), Content: "x"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
}

func TestGRPC_DeleteMessage_Success(t *testing.T) {
	called := false
	svc := editMsgSvc{deleteFn: func(context.Context, domain.ID) error { called = true; return nil }}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)
	if _, err := client.DeleteMessage(authCtx(), &aeropb.DeleteMessageRequest{Id: idgen.NewID().String()}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Delete not called")
	}
}

func TestGRPC_MarkConversationRead_Success(t *testing.T) {
	var gotWith string
	svc := editMsgSvc{convReadFn: func(_ context.Context, with string) error { gotWith = with; return nil }}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)
	if _, err := client.MarkConversationRead(authCtx(), &aeropb.MarkConversationReadRequest{WithUid: "b"}); err != nil {
		t.Fatal(err)
	}
	if gotWith != "b" {
		t.Fatalf("with_uid = %q, want b", gotWith)
	}
}

// ---- file metadata update ----

type updateFileSvc struct {
	service.FileService
	updateFn func(context.Context, domain.ID, service.UpdateFileMetadataInput) (*domain.File, error)
}

func (f updateFileSvc) UpdateMetadata(ctx context.Context, id domain.ID, in service.UpdateFileMetadataInput) (*domain.File, error) {
	return f.updateFn(ctx, id, in)
}

func TestGRPC_UpdateFileMetadata_ChangesFields(t *testing.T) {
	var gotIn service.UpdateFileMetadataInput
	svc := updateFileSvc{updateFn: func(_ context.Context, id domain.ID, in service.UpdateFileMetadataInput) (*domain.File, error) {
		gotIn = in
		return &domain.File{
			ID: id, OwnerUID: "u1", Name: *in.Name,
			Visibility: *in.Visibility, Meta: in.Meta,
		}, nil
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)

	newName := "renamed.txt"
	var vis int32 = 1
	meta, err := structpb.NewStruct(map[string]any{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.UpdateFileMetadata(authCtx(), &aeropb.UpdateFileMetadataRequest{
		Id: idgen.NewID().String(), Name: &newName, Visibility: &vis, Meta: meta,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotIn.Name == nil || *gotIn.Name != "renamed.txt" {
		t.Fatalf("name not forwarded: %+v", gotIn.Name)
	}
	if gotIn.Visibility == nil || *gotIn.Visibility != domain.VisibilityPublic {
		t.Fatalf("visibility not forwarded: %+v", gotIn.Visibility)
	}
	if gotIn.Meta["k"] != "v" {
		t.Fatalf("meta not forwarded: %+v", gotIn.Meta)
	}
	if resp.File.Name != "renamed.txt" || resp.File.Visibility != 1 {
		t.Fatalf("response = %+v", resp.File)
	}
	if resp.File.Meta == nil || resp.File.Meta.AsMap()["k"] != "v" {
		t.Fatalf("response meta = %+v", resp.File.Meta)
	}
}

func TestGRPC_UpdateFileMetadata_OmittedFieldsNil(t *testing.T) {
	var gotIn service.UpdateFileMetadataInput
	svc := updateFileSvc{updateFn: func(_ context.Context, id domain.ID, in service.UpdateFileMetadataInput) (*domain.File, error) {
		gotIn = in
		return &domain.File{ID: id, OwnerUID: "u1", Name: "unchanged"}, nil
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)
	if _, err := client.UpdateFileMetadata(authCtx(), &aeropb.UpdateFileMetadataRequest{Id: idgen.NewID().String()}); err != nil {
		t.Fatal(err)
	}
	if gotIn.Name != nil || gotIn.Visibility != nil || gotIn.Meta != nil {
		t.Fatalf("omitted fields should be nil: %+v", gotIn)
	}
}

func TestGRPC_UpdateFileMetadata_NotFound(t *testing.T) {
	svc := updateFileSvc{updateFn: func(context.Context, domain.ID, service.UpdateFileMetadataInput) (*domain.File, error) {
		return nil, apperr.NotFound("file.not_found", "nope")
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)
	_, err := client.UpdateFileMetadata(authCtx(), &aeropb.UpdateFileMetadataRequest{Id: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPC_UpdateFileMetadata_InvalidID(t *testing.T) {
	conn := dial(t, grpcsrv.NewFileServer(updateFileSvc{}))
	client := aeropb.NewFileServiceClient(conn)
	_, err := client.UpdateFileMetadata(authCtx(), &aeropb.UpdateFileMetadataRequest{Id: "nope"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}
