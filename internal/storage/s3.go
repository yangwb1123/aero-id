package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

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
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		),
	)
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

func (s *S3) Put(ctx context.Context, ownerUID, _ string, r io.Reader) (string, int64, error) {
	// io.Reader → []byte via io.ReadAll for size accounting; service should pre-bound size.
	body, err := io.ReadAll(r)
	if err != nil {
		return "", 0, fmt.Errorf("read body: %w", err)
	}
	key := s.buildKey(ownerUID)
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		return "", 0, fmt.Errorf("put object: %w", err)
	}
	return key, int64(len(body)), nil
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

var _ FileStorage = (*S3)(nil)

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
