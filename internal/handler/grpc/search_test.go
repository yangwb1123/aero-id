package grpc_test

// search_test.go covers the SearchService.Search RPC proto round-trip and the
// MessageService ReactToMessage / UnreactMessage RPCs. Both follow the same
// in-process bufconn harness (dial / authCtx) used by the other edge tests.

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aero/aero-id/internal/domain"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

// ---- SearchService.Search ----

// searchSvc is a fake SearchService capturing the args the handler forwards and
// returning a caller-supplied hit set / error.
type searchSvc struct {
	searchFn func(context.Context, string, []domain.SearchKind, int) ([]domain.SearchHit, error)
}

func (s searchSvc) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]domain.SearchHit, error) {
	return s.searchFn(ctx, query, kinds, limit)
}

func TestGRPC_Search_MapsRequestAndMarshalsHits(t *testing.T) {
	msgID := idgen.NewID()
	commentID := idgen.NewID()
	fileID := idgen.NewID()

	var (
		gotQuery string
		gotKinds []domain.SearchKind
		gotLimit int
	)
	svc := searchSvc{searchFn: func(_ context.Context, q string, kinds []domain.SearchKind, limit int) ([]domain.SearchHit, error) {
		gotQuery, gotKinds, gotLimit = q, kinds, limit
		return []domain.SearchHit{
			{Kind: domain.SearchKindMessages, Message: &domain.Message{ID: msgID, FromUID: "u1", ToUID: "u2", Content: "hi there"}},
			{Kind: domain.SearchKindComments, Comment: &domain.Comment{ID: commentID, OwnerUID: "u3", Content: "nice"}},
			{Kind: domain.SearchKindFiles, File: &domain.File{ID: fileID, OwnerUID: "u4", Name: "doc.txt"}},
		}, nil
	}}
	conn := dial(t, grpcsrv.NewSearchServer(svc))
	client := aeropb.NewSearchServiceClient(conn)

	resp, err := client.Search(authCtx(), &aeropb.SearchRequest{
		Query: "hi",
		Kinds: []string{"messages", "comments", "files"},
		Limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Request mapping: query, kinds, and limit thread through to the service.
	if gotQuery != "hi" {
		t.Fatalf("query = %q, want %q", gotQuery, "hi")
	}
	if gotLimit != 25 {
		t.Fatalf("limit = %d, want 25", gotLimit)
	}
	wantKinds := []domain.SearchKind{domain.SearchKindMessages, domain.SearchKindComments, domain.SearchKindFiles}
	if len(gotKinds) != len(wantKinds) {
		t.Fatalf("kinds = %v, want %v", gotKinds, wantKinds)
	}
	for i := range wantKinds {
		if gotKinds[i] != wantKinds[i] {
			t.Fatalf("kinds[%d] = %q, want %q", i, gotKinds[i], wantKinds[i])
		}
	}

	// Response marshaling: each hit carries its kind string and the matching
	// populated union field.
	if len(resp.Hits) != 3 {
		t.Fatalf("hits = %d, want 3", len(resp.Hits))
	}

	mh := resp.Hits[0]
	if mh.Kind != string(domain.SearchKindMessages) {
		t.Fatalf("hit[0].Kind = %q, want messages", mh.Kind)
	}
	if mh.Message == nil || mh.Message.Id != msgID.String() || mh.Message.Content != "hi there" {
		t.Fatalf("hit[0].Message = %+v", mh.Message)
	}
	if mh.Comment != nil || mh.File != nil {
		t.Fatalf("hit[0] should only populate Message: %+v", mh)
	}

	ch := resp.Hits[1]
	if ch.Kind != string(domain.SearchKindComments) {
		t.Fatalf("hit[1].Kind = %q, want comments", ch.Kind)
	}
	if ch.Comment == nil || ch.Comment.Id != commentID.String() || ch.Comment.Content != "nice" {
		t.Fatalf("hit[1].Comment = %+v", ch.Comment)
	}
	if ch.Message != nil || ch.File != nil {
		t.Fatalf("hit[1] should only populate Comment: %+v", ch)
	}

	fh := resp.Hits[2]
	if fh.Kind != string(domain.SearchKindFiles) {
		t.Fatalf("hit[2].Kind = %q, want files", fh.Kind)
	}
	if fh.File == nil || fh.File.Id != fileID.String() || fh.File.Name != "doc.txt" {
		t.Fatalf("hit[2].File = %+v", fh.File)
	}
	if fh.Message != nil || fh.Comment != nil {
		t.Fatalf("hit[2] should only populate File: %+v", fh)
	}
}

func TestGRPC_Search_UnknownKindsDropped(t *testing.T) {
	var gotKinds []domain.SearchKind
	svc := searchSvc{searchFn: func(_ context.Context, _ string, kinds []domain.SearchKind, _ int) ([]domain.SearchHit, error) {
		gotKinds = kinds
		return nil, nil
	}}
	conn := dial(t, grpcsrv.NewSearchServer(svc))
	client := aeropb.NewSearchServiceClient(conn)

	resp, err := client.Search(authCtx(), &aeropb.SearchRequest{
		Query: "q",
		Kinds: []string{"messages", "bogus", "files"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Hits) != 0 {
		t.Fatalf("hits = %d, want 0", len(resp.Hits))
	}
	// "bogus" is silently skipped by the switch in the handler.
	want := []domain.SearchKind{domain.SearchKindMessages, domain.SearchKindFiles}
	if len(gotKinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", gotKinds, want)
	}
	for i := range want {
		if gotKinds[i] != want[i] {
			t.Fatalf("kinds[%d] = %q, want %q", i, gotKinds[i], want[i])
		}
	}
}

func TestGRPC_Search_EmptyQueryInvalidArgument(t *testing.T) {
	// The service rejects an empty query with apperr.BadRequest; the handler
	// maps that through grpcError to codes.InvalidArgument.
	svc := searchSvc{searchFn: func(context.Context, string, []domain.SearchKind, int) ([]domain.SearchHit, error) {
		return nil, apperr.BadRequest("search.invalid", "query is required")
	}}
	conn := dial(t, grpcsrv.NewSearchServer(svc))
	client := aeropb.NewSearchServiceClient(conn)

	_, err := client.Search(authCtx(), &aeropb.SearchRequest{Query: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// ---- MessageService ReactToMessage / UnreactMessage ----

// reactMsgSvc wires only the reaction methods exercised here.
type reactMsgSvc struct {
	service.MessageService
	reactFn   func(context.Context, domain.ID, string) error
	unreactFn func(context.Context, domain.ID, string) error
}

func (m reactMsgSvc) ReactToMessage(ctx context.Context, id domain.ID, actor string) error {
	return m.reactFn(ctx, id, actor)
}
func (m reactMsgSvc) UnreactMessage(ctx context.Context, id domain.ID, actor string) error {
	return m.unreactFn(ctx, id, actor)
}

func TestGRPC_ReactToMessage_Success(t *testing.T) {
	var (
		gotID    domain.ID
		gotActor string
	)
	svc := reactMsgSvc{reactFn: func(_ context.Context, id domain.ID, actor string) error {
		gotID, gotActor = id, actor
		return nil
	}}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)

	id := idgen.NewID()
	resp, err := client.ReactToMessage(authCtx(), &aeropb.ReactToMessageRequest{MessageId: id.String()})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	if gotID != id {
		t.Fatalf("id = %v, want %v", gotID, id)
	}
	// The validator stub authenticates every request as "u1".
	if gotActor != "u1" {
		t.Fatalf("actor = %q, want u1", gotActor)
	}
}

func TestGRPC_ReactToMessage_InvalidID(t *testing.T) {
	conn := dial(t, grpcsrv.NewMessageServer(reactMsgSvc{}))
	client := aeropb.NewMessageServiceClient(conn)

	_, err := client.ReactToMessage(authCtx(), &aeropb.ReactToMessageRequest{MessageId: "not-a-uuid"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPC_ReactToMessage_NotFound(t *testing.T) {
	svc := reactMsgSvc{reactFn: func(context.Context, domain.ID, string) error {
		return apperr.NotFound("message.not_found", "gone")
	}}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)

	_, err := client.ReactToMessage(authCtx(), &aeropb.ReactToMessageRequest{MessageId: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPC_UnreactMessage_Success(t *testing.T) {
	var (
		gotID    domain.ID
		gotActor string
	)
	svc := reactMsgSvc{unreactFn: func(_ context.Context, id domain.ID, actor string) error {
		gotID, gotActor = id, actor
		return nil
	}}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)

	id := idgen.NewID()
	resp, err := client.UnreactMessage(authCtx(), &aeropb.UnreactMessageRequest{MessageId: id.String()})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	if gotID != id {
		t.Fatalf("id = %v, want %v", gotID, id)
	}
	if gotActor != "u1" {
		t.Fatalf("actor = %q, want u1", gotActor)
	}
}

func TestGRPC_UnreactMessage_InvalidID(t *testing.T) {
	conn := dial(t, grpcsrv.NewMessageServer(reactMsgSvc{}))
	client := aeropb.NewMessageServiceClient(conn)

	_, err := client.UnreactMessage(authCtx(), &aeropb.UnreactMessageRequest{MessageId: "bad"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPC_UnreactMessage_NotFound(t *testing.T) {
	svc := reactMsgSvc{unreactFn: func(context.Context, domain.ID, string) error {
		return apperr.NotFound("message.not_found", "gone")
	}}
	conn := dial(t, grpcsrv.NewMessageServer(svc))
	client := aeropb.NewMessageServiceClient(conn)

	_, err := client.UnreactMessage(authCtx(), &aeropb.UnreactMessageRequest{MessageId: idgen.NewID().String()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}
