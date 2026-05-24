package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

// defaultDownloadTTL is used when a DownloadURL request omits ttl_seconds.
const defaultDownloadTTL = 5 * time.Minute

type FileServer struct {
	aeropb.UnimplementedFileServiceServer
	files service.FileService
}

func NewFileServer(files service.FileService) *FileServer { return &FileServer{files: files} }

func (s *FileServer) Register(srv *grpc.Server) { aeropb.RegisterFileServiceServer(srv, s) }

func (s *FileServer) GetFile(ctx context.Context, req *aeropb.GetFileRequest) (*aeropb.GetFileResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	f, err := s.files.Get(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.GetFileResponse{File: fileToPB(f)}, nil
}

func (s *FileServer) ListFiles(ctx context.Context, req *aeropb.ListFilesRequest) (*aeropb.ListFilesResponse, error) {
	list, err := s.files.List(ctx, req.OwnerUid, pageFromPB(req.Page))
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ListFilesResponse{Files: filesToPB(list.Files), NextCursor: list.NextCursor}, nil
}

func (s *FileServer) DeleteFile(ctx context.Context, req *aeropb.DeleteFileRequest) (*aeropb.DeleteFileResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.files.Delete(ctx, id); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.DeleteFileResponse{}, nil
}

func (s *FileServer) GetDownloadURL(ctx context.Context, req *aeropb.DownloadURLRequest) (*aeropb.DownloadURLResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	ttl := time.Duration(req.TtlSeconds) * time.Second
	if ttl <= 0 {
		ttl = defaultDownloadTTL
	}
	url, err := s.files.DownloadURL(ctx, id, ttl)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.DownloadURLResponse{Url: url}, nil
}

// ---- shared converters (used by message.go and comment.go too) ----

func pageFromPB(p *aeropb.Page) domain.Page {
	if p == nil {
		return domain.Page{}
	}
	return domain.Page{Limit: int(p.Limit), Cursor: p.Cursor}
}

func fileToPB(f *domain.File) *aeropb.File {
	return &aeropb.File{
		Id:          f.ID.String(),
		OwnerUid:    f.OwnerUID,
		Name:        f.Name,
		Size:        f.Size,
		MimeType:    f.MimeType,
		Visibility:  int32(f.Visibility),
		StorageType: f.StorageType,
		CreatedAt:   timestamppb.New(f.CreatedAt),
		UpdatedAt:   timestamppb.New(f.UpdatedAt),
	}
}

func filesToPB(fs []*domain.File) []*aeropb.File {
	out := make([]*aeropb.File, 0, len(fs))
	for _, f := range fs {
		out = append(out, fileToPB(f))
	}
	return out
}
