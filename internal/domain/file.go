package domain

import "strings"
import "time"

// FileStatus tracks the post-upload processing state of a file. Files begin
// as Pending after upload, transition to Ready once all pipeline steps succeed,
// or to Failed if any step errors. Processing means a pipeline worker holds a
// temporary storage ref and GC must skip the file.
type FileStatus int16

const (
	FileStatusPending    FileStatus = 1 // uploaded, awaiting pipeline processing
	FileStatusProcessing FileStatus = 2 // pipeline worker is actively processing
	FileStatusReady      FileStatus = 3 // all pipeline stages completed; file is usable
	FileStatusFailed     FileStatus = 4 // a pipeline stage failed; see Meta["error"]
)

// IsTerminal reports whether the status is a terminal state (Ready or Failed).
// Pending and Processing files are still in-flight.
func (s FileStatus) IsTerminal() bool {
	return s == FileStatusReady || s == FileStatusFailed
}

// FileCategory classifies a file by its content type for UI grouping, search
// filtering, and storage statistics. Categories are derived from MIME types.
type FileCategory string

const (
	CategoryImage    FileCategory = "image"
	CategoryVideo    FileCategory = "video"
	CategoryAudio    FileCategory = "audio"
	CategoryDocument FileCategory = "document"
	CategoryArchive  FileCategory = "archive"
	CategoryOther    FileCategory = "other"
)

// mimeCategoryMap maps known MIME types to their FileCategory. Entries are
// ordered by specificity — the first match wins. Unknown types fall through to
// the detection logic in ClassifyFile.
var mimeCategoryMap = map[string]FileCategory{
	"image/jpeg":         CategoryImage,
	"image/png":          CategoryImage,
	"image/gif":          CategoryImage,
	"image/webp":         CategoryImage,
	"image/bmp":          CategoryImage,
	"image/svg+xml":      CategoryImage,
	"image/tiff":         CategoryImage,
	"image/heic":         CategoryImage,
	"image/heif":         CategoryImage,
	"video/mp4":          CategoryVideo,
	"video/webm":         CategoryVideo,
	"video/ogg":          CategoryVideo,
	"video/quicktime":    CategoryVideo,
	"video/x-msvideo":    CategoryVideo,
	"video/x-matroska":   CategoryVideo,
	"audio/mpeg":         CategoryAudio,
	"audio/ogg":          CategoryAudio,
	"audio/wav":          CategoryAudio,
	"audio/aac":          CategoryAudio,
	"audio/flac":         CategoryAudio,
	"audio/webm":         CategoryAudio,
	"application/pdf":    CategoryDocument,
	"application/msword": CategoryDocument,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": CategoryDocument,
	"application/vnd.ms-excel": CategoryDocument,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         CategoryDocument,
	"application/vnd.ms-powerpoint":                                             CategoryDocument,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": CategoryDocument,
	"text/plain":                   CategoryDocument,
	"text/csv":                     CategoryDocument,
	"text/html":                    CategoryDocument,
	"text/markdown":                CategoryDocument,
	"application/zip":              CategoryArchive,
	"application/gzip":             CategoryArchive,
	"application/x-tar":            CategoryArchive,
	"application/x-7z-compressed":  CategoryArchive,
	"application/x-rar-compressed": CategoryArchive,
}

// ClassifyFile determines the FileCategory from a MIME type string. Unknown
// or unrecognised types fall back to the "other" category. Detection uses the
// top-level type (e.g. "image/*" → image) when the exact MIME is not mapped.
func ClassifyFile(mimeType string) FileCategory {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if mimeType == "" {
		return CategoryOther
	}
	if cat, ok := mimeCategoryMap[mimeType]; ok {
		return cat
	}
	// Fall back to top-level type prefix matching.
	prefix, _, _ := strings.Cut(mimeType, "/")
	switch prefix {
	case "image":
		return CategoryImage
	case "video":
		return CategoryVideo
	case "audio":
		return CategoryAudio
	case "text":
		return CategoryDocument
	default:
		return CategoryOther
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
