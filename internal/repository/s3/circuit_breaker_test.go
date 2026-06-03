package s3_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"
	"github.com/stretchr/testify/require"

	s3repo "gophprofile/internal/repository/s3"
)

func newCB(err error, maxFailures uint32) *s3repo.CBClient {
	return s3repo.NewCBClient(&stubS3{err: err}, maxFailures, 10*time.Second)
}

func TestCBClient_PutObject_OpenAfterMaxFailures(t *testing.T) {
	cb := newCB(errors.New("s3 down"), 3)
	ctx := context.Background()

	for range 3 {
		_ = cb.PutObject(ctx, "key", "image/jpeg", strings.NewReader("x"), 1)
	}
	err := cb.PutObject(ctx, "key", "image/jpeg", strings.NewReader("x"), 1)
	require.ErrorIs(t, err, gobreaker.ErrOpenState)
}

func TestCBClient_PutObject_PassesThroughSuccess(t *testing.T) {
	cb := newCB(nil, 3)
	err := cb.PutObject(context.Background(), "key", "image/jpeg", strings.NewReader("x"), 1)
	require.NoError(t, err)
}

func TestCBClient_GetObject_OpenAfterMaxFailures(t *testing.T) {
	cb := newCB(errors.New("s3 down"), 3)
	ctx := context.Background()

	for range 3 {
		_, _, _, _ = cb.GetObject(ctx, "key")
	}
	_, _, _, err := cb.GetObject(ctx, "key")
	require.ErrorIs(t, err, gobreaker.ErrOpenState)
}

func TestCBClient_DeleteObject_OpenAfterMaxFailures(t *testing.T) {
	cb := newCB(errors.New("s3 down"), 2)
	ctx := context.Background()

	for range 2 {
		_ = cb.DeleteObject(ctx, "key")
	}
	err := cb.DeleteObject(ctx, "key")
	require.ErrorIs(t, err, gobreaker.ErrOpenState)
}

func TestCBClient_IndependentBreakersPerOperation(t *testing.T) {
	cb := newCB(errors.New("err"), 2)
	ctx := context.Background()

	// trip the put breaker
	for range 2 {
		_ = cb.PutObject(ctx, "k", "image/jpeg", strings.NewReader(""), 0)
	}
	require.ErrorIs(t, cb.PutObject(ctx, "k", "image/jpeg", strings.NewReader(""), 0), gobreaker.ErrOpenState)

	// get breaker is still closed — first call returns the stub error, not ErrOpenState
	_, _, _, err := cb.GetObject(ctx, "key")
	require.NotErrorIs(t, err, gobreaker.ErrOpenState)
}

// Compile-time check: CBClient satisfies the avatarStorage interface.
var _ interface {
	PutObject(context.Context, string, string, io.Reader, int64) error
	GetObject(context.Context, string) (io.ReadCloser, int64, string, error)
	DeleteObject(context.Context, string) error
} = (*s3repo.CBClient)(nil)
