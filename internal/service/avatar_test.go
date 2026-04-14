package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"gophprofile/internal/domain"
	"gophprofile/internal/events"
)

type fakeRepo struct {
	createFn   func(ctx context.Context, a *domain.Avatar) error
	lastAvatar *domain.Avatar
	calls      int
}

func (f *fakeRepo) Create(ctx context.Context, a *domain.Avatar) error {
	f.calls++
	f.lastAvatar = a
	if f.createFn != nil {
		return f.createFn(ctx, a)
	}
	return nil
}

type fakeStorage struct {
	putFn    func(ctx context.Context, key, ct string, r io.Reader, size int64) error
	deleteFn func(ctx context.Context, key string) error
	putCalls int
	delCalls int
	lastKey  string
	lastBody []byte
}

func (f *fakeStorage) PutObject(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	f.putCalls++
	f.lastKey = key
	body, _ := io.ReadAll(r)
	f.lastBody = body
	if f.putFn != nil {
		return f.putFn(ctx, key, contentType, bytes.NewReader(body), size)
	}
	return nil
}

func (f *fakeStorage) DeleteObject(ctx context.Context, key string) error {
	f.delCalls++
	if f.deleteFn != nil {
		return f.deleteFn(ctx, key)
	}
	return nil
}

type fakePublisher struct {
	publishFn func(ctx context.Context, evt events.AvatarUploadedEvent) error
	calls     int
	lastEvent events.AvatarUploadedEvent
}

func (f *fakePublisher) PublishAvatarUploaded(ctx context.Context, evt events.AvatarUploadedEvent) error {
	f.calls++
	f.lastEvent = evt
	if f.publishFn != nil {
		return f.publishFn(ctx, evt)
	}
	return nil
}

func validInput() UploadInput {
	body := []byte("fake-jpeg-bytes")
	return UploadInput{
		UserID:      "user-1",
		FileName:    "me.jpg",
		ContentType: "image/jpeg",
		Size:        int64(len(body)),
		Content:     bytes.NewReader(body),
	}
}

func TestAvatarService_Upload_HappyPath(t *testing.T) {
	repo := &fakeRepo{}
	storage := &fakeStorage{}
	pub := &fakePublisher{}
	svc := NewAvatarService(repo, storage, pub)

	a, err := svc.Upload(context.Background(), validInput())
	require.NoError(t, err)
	require.NotNil(t, a)

	require.NotEqual(t, uuid.Nil, a.ID)
	require.Equal(t, "user-1", a.UserID)
	require.Equal(t, "me.jpg", a.FileName)
	require.Equal(t, "image/jpeg", a.MimeType)
	require.Equal(t, domain.UploadStatusUploaded, a.UploadStatus)
	require.Equal(t, domain.ProcessingStatusPending, a.ProcessingStatus)

	require.Equal(t, 1, storage.putCalls)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, 0, storage.delCalls)
	require.Equal(t, 1, pub.calls, "must publish after successful create")
	require.Equal(t, a.ID.String(), pub.lastEvent.AvatarID)
	require.Equal(t, a.UserID, pub.lastEvent.UserID)
	require.Equal(t, a.S3Key, pub.lastEvent.S3Key)

	require.True(t, strings.HasPrefix(a.S3Key, "avatars/user-1/"), "got %q", a.S3Key)
	require.True(t, strings.HasSuffix(a.S3Key, ".jpg"), "got %q", a.S3Key)
	require.Equal(t, a.S3Key, storage.lastKey)
	require.Equal(t, []byte("fake-jpeg-bytes"), storage.lastBody)
}

func TestAvatarService_Upload_RejectsEmptyUserID(t *testing.T) {
	svc := NewAvatarService(&fakeRepo{}, &fakeStorage{}, &fakePublisher{})

	in := validInput()
	in.UserID = ""

	_, err := svc.Upload(context.Background(), in)
	require.ErrorIs(t, err, ErrEmptyUserID)
}

func TestAvatarService_Upload_RejectsInvalidContentType(t *testing.T) {
	repo := &fakeRepo{}
	storage := &fakeStorage{}
	pub := &fakePublisher{}
	svc := NewAvatarService(repo, storage, pub)

	in := validInput()
	in.ContentType = "application/pdf"

	_, err := svc.Upload(context.Background(), in)
	require.ErrorIs(t, err, ErrInvalidContentType)
	require.Equal(t, 0, storage.putCalls, "must not touch storage")
	require.Equal(t, 0, repo.calls, "must not touch repo")
}

func TestAvatarService_Upload_PutFailure_DoesNotCallRepo(t *testing.T) {
	putErr := errors.New("boom")
	repo := &fakeRepo{}
	storage := &fakeStorage{
		putFn: func(ctx context.Context, key, ct string, r io.Reader, size int64) error {
			return putErr
		},
	}
	pub := &fakePublisher{}
	svc := NewAvatarService(repo, storage, pub)

	_, err := svc.Upload(context.Background(), validInput())
	require.ErrorIs(t, err, putErr)
	require.Equal(t, 1, storage.putCalls)
	require.Equal(t, 0, repo.calls)
	require.Equal(t, 0, storage.delCalls)
	require.Equal(t, 0, pub.calls)
}

func TestAvatarService_Upload_RepoFailure_RollsBackS3(t *testing.T) {
	dbErr := errors.New("db down")
	repo := &fakeRepo{
		createFn: func(ctx context.Context, a *domain.Avatar) error { return dbErr },
	}
	storage := &fakeStorage{}
	pub := &fakePublisher{}
	svc := NewAvatarService(repo, storage, pub)

	_, err := svc.Upload(context.Background(), validInput())
	require.ErrorIs(t, err, dbErr)
	require.Equal(t, 1, storage.putCalls)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, 1, storage.delCalls, "rollback must delete the orphaned object")
	require.Equal(t, 0, pub.calls, "must not publish when persistence failed")
}

func TestAvatarService_Upload_UsesFallbackExtensionForUnknownFileName(t *testing.T) {
	repo := &fakeRepo{}
	storage := &fakeStorage{}
	pub := &fakePublisher{}
	svc := NewAvatarService(repo, storage, pub)

	in := validInput()
	in.FileName = "no-extension"

	a, err := svc.Upload(context.Background(), in)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(a.S3Key, ".jpg"), "got %q", a.S3Key)
}

func TestAvatarService_Upload_PublisherFailure_StillReturnsAvatar(t *testing.T) {
	pubErr := errors.New("broker down")
	repo := &fakeRepo{}
	storage := &fakeStorage{}
	pub := &fakePublisher{
		publishFn: func(ctx context.Context, evt events.AvatarUploadedEvent) error { return pubErr },
	}
	svc := NewAvatarService(repo, storage, pub)

	a, err := svc.Upload(context.Background(), validInput())
	require.NoError(t, err, "broker failures must not fail the upload")
	require.NotNil(t, a)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, 0, storage.delCalls, "must not rollback the S3 object")
	require.Equal(t, 1, pub.calls)
}
