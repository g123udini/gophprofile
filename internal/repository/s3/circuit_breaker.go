package s3

import (
	"context"
	"io"
	"time"

	"github.com/sony/gobreaker/v2"
)

// objectStore is the subset of Client methods that CBClient protects.
type objectStore interface {
	PutObject(ctx context.Context, key, contentType string, r io.Reader, size int64) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, int64, string, error)
	DeleteObject(ctx context.Context, key string) error
	Ping(ctx context.Context) error
	Bucket() string
}

// CBClient wraps Client with per-operation circuit breakers.
// When S3 is unavailable, the breaker opens after maxFailures consecutive
// errors and rejects calls immediately until the cooldown passes.
type CBClient struct {
	inner   objectStore
	putCB   *gobreaker.CircuitBreaker[struct{}]
	getCB   *gobreaker.CircuitBreaker[getResult]
	delCB   *gobreaker.CircuitBreaker[struct{}]
	pingCB  *gobreaker.CircuitBreaker[struct{}]
}

type getResult struct {
	rc          io.ReadCloser
	size        int64
	contentType string
}

func NewCBClient(c objectStore, maxFailures uint32, timeout time.Duration) *CBClient {
	settings := func(name string) gobreaker.Settings {
		return gobreaker.Settings{
			Name:        name,
			MaxRequests: 1,
			Interval:    30 * time.Second,
			Timeout:     timeout,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.ConsecutiveFailures >= maxFailures
			},
		}
	}
	return &CBClient{
		inner:  c,
		putCB:  gobreaker.NewCircuitBreaker[struct{}](settings("s3.put")),
		getCB:  gobreaker.NewCircuitBreaker[getResult](settings("s3.get")),
		delCB:  gobreaker.NewCircuitBreaker[struct{}](settings("s3.delete")),
		pingCB: gobreaker.NewCircuitBreaker[struct{}](settings("s3.ping")),
	}
}

func (cb *CBClient) PutObject(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	_, err := cb.putCB.Execute(func() (struct{}, error) {
		return struct{}{}, cb.inner.PutObject(ctx, key, contentType, r, size)
	})
	return err
}

func (cb *CBClient) GetObject(ctx context.Context, key string) (io.ReadCloser, int64, string, error) {
	res, err := cb.getCB.Execute(func() (getResult, error) {
		rc, size, ct, err := cb.inner.GetObject(ctx, key)
		return getResult{rc, size, ct}, err
	})
	return res.rc, res.size, res.contentType, err
}

func (cb *CBClient) DeleteObject(ctx context.Context, key string) error {
	_, err := cb.delCB.Execute(func() (struct{}, error) {
		return struct{}{}, cb.inner.DeleteObject(ctx, key)
	})
	return err
}

func (cb *CBClient) Ping(ctx context.Context) error {
	_, err := cb.pingCB.Execute(func() (struct{}, error) {
		return struct{}{}, cb.inner.Ping(ctx)
	})
	return err
}

func (cb *CBClient) Bucket() string { return cb.inner.Bucket() }
