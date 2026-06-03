package s3_test

import (
	"context"
	"io"
	"strings"
)

type stubS3 struct{ err error }

func (s *stubS3) PutObject(_ context.Context, _, _ string, _ io.Reader, _ int64) error {
	return s.err
}
func (s *stubS3) GetObject(_ context.Context, _ string) (io.ReadCloser, int64, string, error) {
	if s.err != nil {
		return nil, 0, "", s.err
	}
	return io.NopCloser(strings.NewReader("")), 0, "image/jpeg", nil
}
func (s *stubS3) DeleteObject(_ context.Context, _ string) error { return s.err }
func (s *stubS3) Ping(_ context.Context) error                   { return s.err }
func (s *stubS3) Bucket() string                                 { return "test" }
