package file

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
)

const defaultDownloadTTL = 5 * time.Minute

type FileServer struct {
	aeropb.UnimplementedFileServiceServer
	files Service
}

func NewFileServer(files Service) *FileServer { return &FileServer{files: files} }

func (s *FileServer) Register(srv *grpc.Server) { aeropb.RegisterFileServiceServer(srv, s) }

func pageFromPB(p *aeropb.Page) domain.Page {
	if p == nil {
		return domain.Page{}
	}
	return domain.Page{Limit: int(p.Limit), Cursor: p.Cursor}
}

func FileToPB(f *File) *aeropb.File {
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
		if m, err := structpb.NewStruct(f.Meta); err == nil {
			pb.Meta = m
		}
	}
	return pb
}

func filesToPB(fs []*File) []*aeropb.File {
	out := make([]*aeropb.File, 0, len(fs))
	for _, f := range fs {
		out = append(out, FileToPB(f))
	}
	return out
}

func (s *FileServer) GetFile(ctx context.Context, req *aeropb.GetFileRequest) (*aeropb.GetFileResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	f, err := s.files.Get(ctx, id)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.GetFileResponse{File: FileToPB(f)}, nil
}

func (s *FileServer) ListFiles(ctx context.Context, req *aeropb.ListFilesRequest) (*aeropb.ListFilesResponse, error) {
	list, err := s.files.List(ctx, req.OwnerUid, pageFromPB(req.Page))
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ListFilesResponse{Files: filesToPB(list.Files), NextCursor: list.NextCursor}, nil
}

func (s *FileServer) DeleteFile(ctx context.Context, req *aeropb.DeleteFileRequest) (*aeropb.DeleteFileResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.files.Delete(ctx, id); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.DeleteFileResponse{}, nil
}

func (s *FileServer) GetDownloadURL(ctx context.Context, req *aeropb.DownloadURLRequest) (*aeropb.DownloadURLResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	ttl := time.Duration(req.TtlSeconds) * time.Second
	if ttl <= 0 {
		ttl = defaultDownloadTTL
	}
	url, err := s.files.DownloadURL(ctx, id, ttl)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.DownloadURLResponse{Url: url}, nil
}

func (s *FileServer) ShareFile(ctx context.Context, req *aeropb.ShareFileRequest) (*aeropb.ShareFileResponse, error) {
	id, err := idgen.Parse(req.FileId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid file_id"))
	}
	if err := s.files.ShareFile(ctx, id, req.GranteeUids); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ShareFileResponse{}, nil
}

func (s *FileServer) UnshareFile(ctx context.Context, req *aeropb.UnshareFileRequest) (*aeropb.UnshareFileResponse, error) {
	id, err := idgen.Parse(req.FileId)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid file_id"))
	}
	if err := s.files.UnshareFile(ctx, id, req.GranteeUid); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.UnshareFileResponse{}, nil
}

func (s *FileServer) ListSharedWithMe(ctx context.Context, req *aeropb.ListSharedWithMeRequest) (*aeropb.ListFilesResponse, error) {
	list, err := s.files.ListSharedWithMe(ctx, pageFromPB(req.Page))
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ListFilesResponse{Files: filesToPB(list.Files), NextCursor: list.NextCursor}, nil
}

func (s *FileServer) PresignUpload(ctx context.Context, req *aeropb.PresignUploadRequest) (*aeropb.PresignUploadResponse, error) {
	rawVis := req.Visibility
	if rawVis != int32(domain.VisibilityPrivate) && rawVis != int32(domain.VisibilityPublic) {
		return nil, apperr.GRPCError(apperr.BadRequest("file.invalid_visibility", "invalid visibility"))
	}
	res, err := s.files.PresignUpload(ctx, PresignUploadInput{
		Name:       req.Name,
		MimeType:   req.MimeType,
		Visibility: domain.Visibility(rawVis),
	}, defaultDownloadTTL)
	if err != nil {
		return nil, apperr.GRPCError(err)
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
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid file_id"))
	}
	rawVis := req.Visibility
	if rawVis != int32(domain.VisibilityPrivate) && rawVis != int32(domain.VisibilityPublic) {
		return nil, apperr.GRPCError(apperr.BadRequest("file.invalid_visibility", "invalid visibility"))
	}
	f, err := s.files.CompleteUpload(ctx, CompleteUploadInput{
		FileID:     fileID,
		StorageKey: req.StorageKey,
		Name:       req.Name,
		MimeType:   req.MimeType,
		Visibility: domain.Visibility(rawVis),
	})
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.CompleteUploadResponse{File: FileToPB(f)}, nil
}

func (s *FileServer) UpdateFileMetadata(ctx context.Context, req *aeropb.UpdateFileMetadataRequest) (*aeropb.UpdateFileMetadataResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, apperr.GRPCError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	in := UpdateFileMetadataInput{Name: req.Name}
	if req.Visibility != nil {
		raw := *req.Visibility
		if raw != int32(domain.VisibilityPrivate) && raw != int32(domain.VisibilityPublic) {
			return nil, apperr.GRPCError(apperr.BadRequest("file.invalid_visibility", "invalid visibility"))
		}
		v := domain.Visibility(raw)
		in.Visibility = &v
	}
	if req.Meta != nil {
		in.Meta = req.Meta.AsMap()
	}
	f, err := s.files.UpdateMetadata(ctx, id, in)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.UpdateFileMetadataResponse{File: FileToPB(f)}, nil
}
