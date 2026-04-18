// Package s3 implements an AWS S3 store.Target.
//
// Uploads use the PutObject single-shot path; multipart uploads are out of
// scope for v0.4.0 per the CEO-locked dependency constraint (no
// feature/s3/manager sub-module). Region detection prefers AWS_REGION, then
// the default SDK config chain, then falls back to us-east-1 with a warn-
// level log entry.
package s3

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// Name is the Target identifier reported by S3Target.Name.
const Name = "s3"

// fallbackRegion is used only when AWS_REGION is unset and the default SDK
// config chain does not yield a region. us-east-1 matches the AWS default.
const fallbackRegion = "us-east-1"

// Env vars consulted for region detection.
const (
	envAWSRegion        = "AWS_REGION"
	envAWSDefaultRegion = "AWS_DEFAULT_REGION"
)

// Sentinel errors returned by the S3 target.
var (
	// ErrMissingSecret indicates the secret key was not supplied.
	ErrMissingSecret = errors.New("s3: missing required secret")
	// ErrInvalidConfig indicates a structural problem with cfg.
	ErrInvalidConfig = errors.New("s3: invalid config")
)

// uploader is the narrowed PutObject surface the target depends on. It is
// satisfied by *awss3.Client and is exported (as an interface) only inside
// the package for testability.
type uploader interface {
	PutObject(ctx context.Context, in *awss3.PutObjectInput,
		optFns ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
}

// S3Target uploads archives to a single S3 bucket under a configured prefix.
type S3Target struct {
	client uploader
	bucket string
	prefix string
	logger *slog.Logger
}

// New constructs an S3Target. Access credentials come from cfg.S3AccessKey
// (public) and secrets.S3SecretKey (private). The AWS SDK loads additional
// shared config from the environment; the caller's explicit static
// credentials override anything in ~/.aws/credentials for this client.
func New(cfg config.S3SaveSection, secrets config.Secrets) (*S3Target, error) {
	if cfg.BucketName == "" {
		return nil, fmt.Errorf("%w: bucket_name must not be empty", ErrInvalidConfig)
	}
	if cfg.S3AccessKey == "" {
		return nil, fmt.Errorf("%w: s3_access_key must not be empty", ErrInvalidConfig)
	}
	if secrets.S3SecretKey == "" {
		return nil, fmt.Errorf("%w: S3 secret key", ErrMissingSecret)
	}

	logger := slog.Default()
	region := resolveRegion(context.Background(), logger)

	provider := credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, secrets.S3SecretKey, "")

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(provider),
	)
	if err != nil {
		return nil, fmt.Errorf("s3: load aws config: %w", err)
	}

	return &S3Target{
		client: awss3.NewFromConfig(awsCfg),
		bucket: cfg.BucketName,
		prefix: strings.Trim(cfg.SavePath, "/"),
		logger: logger,
	}, nil
}

// Name implements store.Target.
func (t *S3Target) Name() string { return Name }

// Upload implements store.Target. The object key is joined as
// "<prefix>/<remoteName>" with any empty prefix collapsed. The file is
// streamed to S3 as the request body; the SDK uses a single PutObject call
// (no multipart), so very large archives are bounded by the single-request
// limit (5 GiB for PUT, with practical 100 GiB ceiling via multipart left
// as future work).
func (t *S3Target) Upload(ctx context.Context, localPath, remoteName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if localPath == "" || remoteName == "" {
		return errors.New("s3 upload: localPath and remoteName must be non-empty")
	}

	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("s3 upload: open %s: %w", localPath, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("s3 upload: stat %s: %w", localPath, err)
	}

	key := t.objectKey(remoteName)
	size := info.Size()

	if _, err := t.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:        aws.String(t.bucket),
		Key:           aws.String(key),
		Body:          f,
		ContentLength: aws.Int64(size),
	}); err != nil {
		return fmt.Errorf("s3 upload: put %s/%s: %w", t.bucket, key, err)
	}
	return nil
}

// objectKey joins the prefix and remoteName, trimming separators so the
// result never contains duplicate slashes and never starts with '/'.
func (t *S3Target) objectKey(remoteName string) string {
	name := strings.TrimLeft(remoteName, "/")
	if t.prefix == "" {
		return name
	}
	return path.Join(t.prefix, name)
}

// resolveRegion returns the AWS region to use for the S3 client, in order:
// AWS_REGION, AWS_DEFAULT_REGION, the SDK's default config chain (shared
// config files), and finally fallbackRegion with a warn log.
func resolveRegion(ctx context.Context, logger *slog.Logger) string {
	if r := os.Getenv(envAWSRegion); r != "" {
		return r
	}
	if r := os.Getenv(envAWSDefaultRegion); r != "" {
		return r
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err == nil && cfg.Region != "" {
		return cfg.Region
	}
	logger.Warn("s3 region not detected, falling back",
		"fallback", fallbackRegion)
	return fallbackRegion
}
