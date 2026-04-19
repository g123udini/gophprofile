package s3

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"gophprofile/internal/config"
)

var ErrObjectNotFound = errors.New("s3 object not found")

type Client struct {
	mc     *minio.Client
	bucket string
}

func NewClient(cfg config.S3Config) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: new client: %w", err)
	}
	return &Client{mc: mc, bucket: cfg.Bucket}, nil
}

func (c *Client) Bucket() string {
	return c.bucket
}

func (c *Client) EnsureBucket(ctx context.Context) error {
	exists, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("s3: bucket exists: %w", err)
	}
	if exists {
		return nil
	}
	if err := c.mc.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("s3: make bucket: %w", err)
	}
	return nil
}

func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.mc.BucketExists(ctx, c.bucket); err != nil {
		return fmt.Errorf("s3: ping: %w", err)
	}
	return nil
}

func (c *Client) PutObject(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("s3: put %q: %w", key, err)
	}
	return nil
}

func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, int64, string, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, "", fmt.Errorf("s3: get %q: %w", key, err)
	}
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		if isNotFound(err) {
			return nil, 0, "", ErrObjectNotFound
		}
		return nil, 0, "", fmt.Errorf("s3: stat %q: %w", key, err)
	}
	return obj, info.Size, info.ContentType, nil
}

func (c *Client) DeleteObject(ctx context.Context, key string) error {
	if err := c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("s3: delete %q: %w", key, err)
	}
	return nil
}

func isNotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.StatusCode == 404
}
