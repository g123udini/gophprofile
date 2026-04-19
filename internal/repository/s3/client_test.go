package s3_test

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"gophprofile/internal/config"
	"gophprofile/internal/repository/s3"
)

var testCfg config.S3Config

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ctr, err := tcminio.Run(ctx,
		"minio/minio:RELEASE.2024-01-16T16-07-38Z",
		tcminio.WithUsername("testuser"),
		tcminio.WithPassword("testpass12345"),
	)
	if err != nil {
		log.Printf("start minio container: %v", err)
		return 1
	}
	defer func() {
		_ = ctr.Terminate(context.Background())
	}()

	endpoint, err := ctr.ConnectionString(ctx)
	if err != nil {
		log.Printf("connection string: %v", err)
		return 1
	}

	testCfg = config.S3Config{
		Endpoint:  endpoint,
		AccessKey: "testuser",
		SecretKey: "testpass12345",
		Bucket:    "gophprofile-test",
		UseSSL:    false,
	}

	return m.Run()
}

func newClient(t *testing.T) *s3.Client {
	t.Helper()
	c, err := s3.NewClient(testCfg)
	require.NoError(t, err)
	require.NoError(t, c.EnsureBucket(context.Background()))
	return c
}

func TestClient_EnsureBucket_Idempotent(t *testing.T) {
	c, err := s3.NewClient(testCfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, c.EnsureBucket(ctx))
	require.NoError(t, c.EnsureBucket(ctx), "second call on existing bucket must be a no-op")
}

func TestClient_PutGetRoundTrip(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	payload := []byte("hello avatar bytes")
	key := "round-trip/hello.txt"

	require.NoError(t, c.PutObject(ctx, key, "text/plain", bytes.NewReader(payload), int64(len(payload))))

	rc, size, contentType, err := c.GetObject(ctx, key)
	require.NoError(t, err)
	defer rc.Close()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, payload, got)
	require.Equal(t, int64(len(payload)), size)
	require.Equal(t, "text/plain", contentType)
}

func TestClient_DeleteObject_RemovesIt(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	key := "to-delete/file.bin"
	require.NoError(t, c.PutObject(ctx, key, "application/octet-stream",
		bytes.NewReader([]byte("x")), 1))

	require.NoError(t, c.DeleteObject(ctx, key))

	_, _, _, err := c.GetObject(ctx, key)
	require.ErrorIs(t, err, s3.ErrObjectNotFound)
}

func TestClient_GetObject_NotFound(t *testing.T) {
	c := newClient(t)

	_, _, _, err := c.GetObject(context.Background(), "missing/key.jpg")
	require.ErrorIs(t, err, s3.ErrObjectNotFound)
}

func TestClient_Ping(t *testing.T) {
	c := newClient(t)
	require.NoError(t, c.Ping(context.Background()))
}
