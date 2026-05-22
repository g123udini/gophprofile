package s3

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"gophprofile/internal/config"
)

var ErrObjectNotFound = errors.New("s3 object not found")

const tracerName = "gophprofile/s3"

type Client struct {
	mc     *minio.Client
	bucket string
	tracer trace.Tracer
}

func NewClient(cfg config.S3Config) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: new client: %w", err)
	}
	return &Client{
		mc:     mc,
		bucket: cfg.Bucket,
		tracer: otel.Tracer(tracerName),
	}, nil
}

// startSpan opens a client-kind span with stable peer attributes. Callers add
// op-specific attrs (key, size, content_type) and record errors before End.
func (c *Client) startSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return c.tracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "s3"),
			attribute.String("s3.bucket", c.bucket),
		),
	)
}

func recordSpanErr(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

func (c *Client) Bucket() string {
	return c.bucket
}

func (c *Client) EnsureBucket(ctx context.Context) (err error) {
	ctx, span := c.startSpan(ctx, "s3.EnsureBucket")
	defer func() { recordSpanErr(span, err); span.End() }()

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

func (c *Client) Ping(ctx context.Context) (err error) {
	ctx, span := c.startSpan(ctx, "s3.Ping")
	defer func() { recordSpanErr(span, err); span.End() }()

	if _, err := c.mc.BucketExists(ctx, c.bucket); err != nil {
		return fmt.Errorf("s3: ping: %w", err)
	}
	return nil
}

func (c *Client) PutObject(ctx context.Context, key, contentType string, r io.Reader, size int64) (err error) {
	ctx, span := c.startSpan(ctx, "s3.PutObject")
	span.SetAttributes(
		attribute.String("s3.key", key),
		attribute.String("s3.content_type", contentType),
		attribute.Int64("s3.size", size),
	)
	defer func() { recordSpanErr(span, err); span.End() }()

	_, err = c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("s3: put %q: %w", key, err)
	}
	return nil
}

func (c *Client) GetObject(ctx context.Context, key string) (rc io.ReadCloser, size int64, contentType string, err error) {
	ctx, span := c.startSpan(ctx, "s3.GetObject")
	span.SetAttributes(attribute.String("s3.key", key))
	defer func() { recordSpanErr(span, err); span.End() }()

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
	span.SetAttributes(attribute.Int64("s3.size", info.Size))
	return obj, info.Size, info.ContentType, nil
}

func (c *Client) DeleteObject(ctx context.Context, key string) (err error) {
	ctx, span := c.startSpan(ctx, "s3.DeleteObject")
	span.SetAttributes(attribute.String("s3.key", key))
	defer func() { recordSpanErr(span, err); span.End() }()

	if err := c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("s3: delete %q: %w", key, err)
	}
	return nil
}

func isNotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.StatusCode == 404
}
