package domain

import "strings"
import "time"

// FileStatus tracks the post-upload processing state of a file. Files begin
// as pending after upload, transition to ready once all pipeline steps succeed,
// or to failed if any step errors. Processing means a pipeline worker holds a
// temporary storage ref and GC must skip the file.
//
// The status values (1-4) are persisted in rows; they are private to the
// domain because no caller outside the package needs the symbolic names.
type FileStatus int16

const (
	fileStatusPending    FileStatus = 1 // uploaded, awaiting pipeline processing
	fileStatusProcessing FileStatus = 2 // pipeline worker is actively processing
	fileStatusReady      FileStatus = 3 // all pipeline stages completed; file is usable
	fileStatusFailed     FileStatus = 4 // a pipeline stage failed; see Meta["error"]
)

// IsTerminal reports whether the status is a terminal state (ready or failed).
// Pending and processing files are still in-flight.
func (s FileStatus) IsTerminal() bool {
	return s == fileStatusReady || s == fileStatusFailed
}

// FileCategory classifies a file by its content type for UI grouping, search
// filtering, and storage statistics. Categories are derived from MIME types.
type FileCategory string

const (
	categoryImage    FileCategory = "image"
	categoryVideo    FileCategory = "video"
	categoryAudio    FileCategory = "audio"
	categoryDocument FileCategory = "document"
	categoryArchive  FileCategory = "archive"
	categoryOther    FileCategory = "other"
)

// mimeCategoryMap maps known MIME types to their FileCategory. Entries are
// ordered by specificity — the first match wins. Unknown types fall through to
// the detection logic in ClassifyFile.
var mimeCategoryMap = map[string]FileCategory{
	"image/jpeg":         categoryImage,
	"image/png":          categoryImage,
	"image/gif":          categoryImage,
	"image/webp":         categoryImage,
	"image/bmp":          categoryImage,
	"image/svg+xml":      categoryImage,
	"image/tiff":         categoryImage,
	"image/heic":         categoryImage,
	"image/heif":         categoryImage,
	"video/mp4":          categoryVideo,
	"video/webm":         categoryVideo,
	"video/ogg":          categoryVideo,
	"video/quicktime":    categoryVideo,
	"video/x-msvideo":    categoryVideo,
	"video/x-matroska":   categoryVideo,
	"audio/mpeg":         categoryAudio,
	"audio/ogg":          categoryAudio,
	"audio/wav":          categoryAudio,
	"audio/aac":          categoryAudio,
	"audio/flac":         categoryAudio,
	"audio/webm":         categoryAudio,
	"application/pdf":    categoryDocument,
	"application/msword": categoryDocument,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": categoryDocument,
	"application/vnd.ms-excel": categoryDocument,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         categoryDocument,
	"application/vnd.ms-powerpoint":                                             categoryDocument,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": categoryDocument,
	"text/plain":                   categoryDocument,
	"text/csv":                     categoryDocument,
	"text/html":                    categoryDocument,
	"text/markdown":                categoryDocument,
	"application/zip":              categoryArchive,
	"application/gzip":             categoryArchive,
	"application/x-tar":            categoryArchive,
	"application/x-7z-compressed":  categoryArchive,
	"application/x-rar-compressed": categoryArchive,
}

// ClassifyFile determines the FileCategory from a MIME type string. Unknown
// or unrecognised types fall back to the "other" category. Detection uses the
// top-level type (e.g. "image/*" → image) when the exact MIME is not mapped.
func ClassifyFile(mimeType string) FileCategory {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if mimeType == "" {
		return categoryOther
	}
	if cat, ok := mimeCategoryMap[mimeType]; ok {
		return cat
	}
	// Fall back to top-level type prefix matching.
	prefix, _, _ := strings.Cut(mimeType, "/")
	switch prefix {
	case "image":
		return categoryImage
	case "video":
		return categoryVideo
	case "audio":
		return categoryAudio
	case "text":
		return categoryDocument
	default:
		return categoryOther
	}
}

type File struct {
	ID          ID
	OwnerUID    string
	Name        string
	Size        int64
	MimeType    string
	Category    FileCategory // derived from MimeType on upload; "other" by default
	Status      FileStatus   // post-upload processing state; Pending by default
	StorageKey  string
	StorageType string
	Checksum    string
	Visibility  Visibility
	Meta        map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
