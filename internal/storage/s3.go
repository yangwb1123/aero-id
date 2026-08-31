package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type S3Config struct {
	Region       string
	Bucket       string
	Endpoint     string // optional; for MinIO/aliyun OSS
	AccessKey    string
	SecretKey    string
	UsePathStyle bool
}

type S3 struct {
	cfg    S3Config
	client *s3.Client
}

func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	if (strings.TrimSpace(cfg.AccessKey) == "") != (strings.TrimSpace(cfg.SecretKey) == "") {
		return nil, errors.New("s3 access key and secret key must be configured together")
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	// Empty credentials deliberately use the AWS SDK's default provider chain
	// (for example workload identity or an instance role). Static credentials
	// are only installed when the caller supplied a complete pair.
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.UsePathStyle
	})
	return &S3{cfg: cfg, client: client}, nil
}

func (s *S3) buildKey(ownerUID string) string {
	now := time.Now().UTC()
	return fmt.Sprintf("%s/%s/%s/%s", ownerUID, now.Format("2006"), now.Format("01"), idgen.NewID())
}

// NewKey allocates an owner-scoped storage key without touching S3, so a presigned
// direct upload and its later finalize agree on where the object lives.
func (s *S3) NewKey(ownerUID string) string { return s.buildKey(ownerUID) }

// countingReader records how many bytes have flowed through it so the streaming
// upload can report the object size without buffering the body in memory.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (s *S3) Put(ctx context.Context, ownerUID, _ string, r io.Reader) (string, int64, error) {
	return s.put(ctx, s.buildKey(ownerUID), r)
}

func (s *S3) PutJobArtifact(ctx context.Context, ownerUID string, jobID domain.ID, kind string, r io.Reader) (string, int64, error) {
	key, err := JobArtifactKey(ownerUID, jobID, kind)
	if err != nil {
		return "", 0, err
	}
	return s.put(ctx, key, r)
}

func (s *S3) put(ctx context.Context, key string, r io.Reader) (string, int64, error) {
	// Stream straight to S3 with the multipart manager so memory stays bounded.
	cr := &countingReader{r: r}
	uploader := manager.NewUploader(s.client)
	_, err := uploader.Upload(ctx, &s3.PutObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key), Body: cr})
	if err != nil {
		_ = s.Delete(ctx, key)
		return "", 0, fmt.Errorf("upload object: %w", err)
	}
	return key, cr.n, nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get object: %w", err)
	}
	return out.Body, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

func (s *S3) Stat(ctx context.Context, key string) (*ObjectInfo, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("head object: %w", err)
	}
	info := &ObjectInfo{Key: key}
	if out.ContentLength != nil {
		info.Size = *out.ContentLength
	}
	if out.ETag != nil {
		info.ETag = *out.ETag
	}
	if out.LastModified != nil {
		info.Modified = *out.LastModified
	}
	return info, nil
}

func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	presigner := s3.NewPresignClient(s.client)
	req, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign: %w", err)
	}
	return req.URL, nil
}

// PresignPut returns a time-limited URL the client can PUT an object to directly,
// letting large uploads bypass the app process entirely.
func (s *S3) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	presigner := s3.NewPresignClient(s.client)
	req, err := presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign put: %w", err)
	}
	return req.URL, nil
}

// List enumerates every object in the bucket via the ListObjectsV2 paginator and
// reports each with its key and last-modified time. Implements Lister so the GC sweep
// can detect orphaned blobs. Iteration honors ctx cancellation between pages.
func (s *S3) List(ctx context.Context, fn func(ObjectInfo) (bool, error)) error {
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.Bucket),
	})
	for paginator.HasMorePages() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list objects: %w", err)
		}
		for _, obj := range page.Contents {
			cont, ferr := fn(s3ObjectInfo(obj))
			if ferr != nil {
				return ferr
			}
			if !cont {
				return nil
			}
		}
	}
	return nil
}

func s3ObjectInfo(obj s3types.Object) ObjectInfo {
	info := ObjectInfo{}
	if obj.Key != nil {
		info.Key = *obj.Key
	}
	if obj.Size != nil {
		info.Size = *obj.Size
	}
	if obj.ETag != nil {
		info.ETag = *obj.ETag
	}
	if obj.LastModified != nil {
		info.Modified = obj.LastModified.UTC()
	}
	return info
}

var (
	_ FileStorage = (*S3)(nil)
	_ Lister      = (*S3)(nil)
)

// EnsureBucket idempotently creates the configured bucket. Useful for tests.
func (s *S3) EnsureBucket(ctx context.Context) error {
	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(s.cfg.Bucket),
	})
	if err == nil {
		return nil
	}
	var bne *s3types.BucketAlreadyOwnedByYou
	var bex *s3types.BucketAlreadyExists
	if errors.As(err, &bne) || errors.As(err, &bex) {
		return nil
	}
	return fmt.Errorf("create bucket: %w", err)
}
