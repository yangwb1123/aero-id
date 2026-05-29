package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
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

func (s *FileServer) ShareFile(ctx context.Context, req *aeropb.ShareFileRequest) (*aeropb.ShareFileResponse, error) {
	id, err := idgen.Parse(req.FileId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid file_id"))
	}
	if err := s.files.ShareFile(ctx, id, req.GranteeUids); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ShareFileResponse{}, nil
}

func (s *FileServer) UnshareFile(ctx context.Context, req *aeropb.UnshareFileRequest) (*aeropb.UnshareFileResponse, error) {
	id, err := idgen.Parse(req.FileId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid file_id"))
	}
	if err := s.files.UnshareFile(ctx, id, req.GranteeUid); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.UnshareFileResponse{}, nil
}

func (s *FileServer) ListSharedWithMe(ctx context.Context, req *aeropb.ListSharedWithMeRequest) (*aeropb.ListFilesResponse, error) {
	list, err := s.files.ListSharedWithMe(ctx, pageFromPB(req.Page))
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ListFilesResponse{Files: filesToPB(list.Files), NextCursor: list.NextCursor}, nil
}

func (s *FileServer) PresignUpload(ctx context.Context, req *aeropb.PresignUploadRequest) (*aeropb.PresignUploadResponse, error) {
	res, err := s.files.PresignUpload(ctx, service.PresignUploadInput{
		Name:       req.Name,
		MimeType:   req.MimeType,
		Visibility: domain.Visibility(req.Visibility),
	}, defaultDownloadTTL)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.PresignUploadResponse{
		FileId:     res.FileID.String(),
		UploadUrl:  res.UploadURL,
		StorageKey: res.StorageKey,
	}, nil
}

func (s *FileServer) CompleteUpload(ctx context.Context, req *aeropb.CompleteUploadRequest) (*aeropb.CompleteUploadResponse, error) {
	fileID, err := idgen.Parse(req.FileId)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid file_id"))
	}
	f, err := s.files.CompleteUpload(ctx, service.CompleteUploadInput{
		FileID:     fileID,
		StorageKey: req.StorageKey,
		Name:       req.Name,
		MimeType:   req.MimeType,
		Visibility: domain.Visibility(req.Visibility),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.CompleteUploadResponse{File: fileToPB(f)}, nil
}

// UpdateFileMetadata mirrors the HTTP PATCH field-mask semantics: only the fields
// present on the request are applied. name/visibility are proto3 optional (pointer
// presence); meta, when set, is merged into the file's existing meta map.
func (s *FileServer) UpdateFileMetadata(ctx context.Context, req *aeropb.UpdateFileMetadataRequest) (*aeropb.UpdateFileMetadataResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	in := service.UpdateFileMetadataInput{Name: req.Name}
	if req.Visibility != nil {
		v := domain.Visibility(*req.Visibility)
		in.Visibility = &v
	}
	if req.Meta != nil {
		in.Meta = req.Meta.AsMap()
	}
	f, err := s.files.UpdateMetadata(ctx, id, in)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.UpdateFileMetadataResponse{File: fileToPB(f)}, nil
}

// ---- shared converters (used by message.go and comment.go too) ----

func pageFromPB(p *aeropb.Page) domain.Page {
	if p == nil {
		return domain.Page{}
	}
	return domain.Page{Limit: int(p.Limit), Cursor: p.Cursor}
}

func fileToPB(f *domain.File) *aeropb.File {
	pb := &aeropb.File{
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
	if len(f.Meta) > 0 {
		// Meta originates from JSON, so it is always struct-encodable; on the
		// off chance it is not, drop it rather than fail the whole response.
		if m, err := structpb.NewStruct(f.Meta); err == nil {
			pb.Meta = m
		}
	}
	return pb
}

func filesToPB(fs []*domain.File) []*aeropb.File {
	out := make([]*aeropb.File, 0, len(fs))
	for _, f := range fs {
		out = append(out, fileToPB(f))
	}
	return out
}
