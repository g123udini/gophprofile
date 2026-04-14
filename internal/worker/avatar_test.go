package worker

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"gophprofile/internal/domain"
	"gophprofile/internal/events"
)

func makeTestJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	return buf.Bytes()
}

type fakeStore struct {
	getBody      []byte
	getErr       error
	putFn        func(ctx context.Context, key, ct string, r io.Reader, size int64) error
	putCalls     int
	uploaded     map[string][]byte
}

func (f *fakeStore) GetObject(ctx context.Context, key string) (io.ReadCloser, int64, string, error) {
	if f.getErr != nil {
		return nil, 0, "", f.getErr
	}
	return io.NopCloser(bytes.NewReader(f.getBody)), int64(len(f.getBody)), "image/jpeg", nil
}

func (f *fakeStore) PutObject(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	f.putCalls++
	body, _ := io.ReadAll(r)
	if f.uploaded == nil {
		f.uploaded = map[string][]byte{}
	}
	f.uploaded[key] = body
	if f.putFn != nil {
		return f.putFn(ctx, key, contentType, bytes.NewReader(body), size)
	}
	return nil
}

type fakeRepo struct {
	updateFn    func(ctx context.Context, id uuid.UUID, status string, thumbs map[string]string) error
	calls       int
	lastID      uuid.UUID
	lastStatus  string
	lastThumbs  map[string]string
}

func (f *fakeRepo) UpdateProcessing(ctx context.Context, id uuid.UUID, status string, thumbs map[string]string) error {
	f.calls++
	f.lastID = id
	f.lastStatus = status
	f.lastThumbs = thumbs
	if f.updateFn != nil {
		return f.updateFn(ctx, id, status, thumbs)
	}
	return nil
}

func TestAvatarProcessor_HandleUploaded_HappyPath(t *testing.T) {
	store := &fakeStore{getBody: makeTestJPEG(t, 640, 480)}
	repo := &fakeRepo{}
	p := NewAvatarProcessor(store, repo)

	id := uuid.New()
	err := p.HandleUploaded(context.Background(), events.AvatarUploadedEvent{
		AvatarID: id.String(),
		UserID:   "u1",
		S3Key:    "avatars/u1/src.jpg",
	})
	require.NoError(t, err)

	require.Equal(t, 2, store.putCalls)
	require.Contains(t, store.uploaded, "thumbnails/"+id.String()+"/100x100.jpg")
	require.Contains(t, store.uploaded, "thumbnails/"+id.String()+"/300x300.jpg")

	for size, w := range map[string]int{"100x100": 100, "300x300": 300} {
		body := store.uploaded["thumbnails/"+id.String()+"/"+size+".jpg"]
		img, err := jpeg.Decode(bytes.NewReader(body))
		require.NoError(t, err, "thumbnail %s must be valid JPEG", size)
		require.Equal(t, w, img.Bounds().Dx(), "thumbnail %s width", size)
		require.Equal(t, w, img.Bounds().Dy(), "thumbnail %s height", size)
	}

	require.Equal(t, 1, repo.calls)
	require.Equal(t, id, repo.lastID)
	require.Equal(t, domain.ProcessingStatusCompleted, repo.lastStatus)
	require.Equal(t, "thumbnails/"+id.String()+"/100x100.jpg", repo.lastThumbs["100x100"])
	require.Equal(t, "thumbnails/"+id.String()+"/300x300.jpg", repo.lastThumbs["300x300"])
}

func TestAvatarProcessor_HandleUploaded_InvalidUUID(t *testing.T) {
	p := NewAvatarProcessor(&fakeStore{}, &fakeRepo{})
	err := p.HandleUploaded(context.Background(), events.AvatarUploadedEvent{
		AvatarID: "not-a-uuid",
	})
	require.Error(t, err)
}

func TestAvatarProcessor_HandleUploaded_GetObjectFails(t *testing.T) {
	store := &fakeStore{getErr: errors.New("s3 unreachable")}
	repo := &fakeRepo{}
	p := NewAvatarProcessor(store, repo)

	err := p.HandleUploaded(context.Background(), events.AvatarUploadedEvent{
		AvatarID: uuid.New().String(), S3Key: "x",
	})
	require.Error(t, err)
	require.Equal(t, 0, repo.calls, "must not update db when download failed")
}

func TestAvatarProcessor_HandleUploaded_BrokenImage_MarksFailed(t *testing.T) {
	store := &fakeStore{getBody: []byte("not an image")}
	repo := &fakeRepo{}
	p := NewAvatarProcessor(store, repo)

	err := p.HandleUploaded(context.Background(), events.AvatarUploadedEvent{
		AvatarID: uuid.New().String(), S3Key: "x",
	})
	require.Error(t, err)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, domain.ProcessingStatusFailed, repo.lastStatus)
}

func TestAvatarProcessor_HandleUploaded_UploadFails_MarksFailed(t *testing.T) {
	uploadErr := errors.New("put boom")
	store := &fakeStore{
		getBody: makeTestJPEG(t, 200, 200),
		putFn: func(ctx context.Context, key, ct string, r io.Reader, size int64) error {
			return uploadErr
		},
	}
	repo := &fakeRepo{}
	p := NewAvatarProcessor(store, repo)

	err := p.HandleUploaded(context.Background(), events.AvatarUploadedEvent{
		AvatarID: uuid.New().String(), S3Key: "x",
	})
	require.ErrorIs(t, err, uploadErr)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, domain.ProcessingStatusFailed, repo.lastStatus)
}
