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
	createFn     func(ctx context.Context, a *domain.Avatar) error
	getFn        func(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	listFn       func(ctx context.Context, userID string) ([]*domain.Avatar, error)
	latestFn     func(ctx context.Context, userID string) (*domain.Avatar, error)
	softDeleteFn func(ctx context.Context, id uuid.UUID) error
	lastAvatar   *domain.Avatar
	lastLimit    int
	lastOffset   int
	calls        int
}

func (f *fakeRepo) Create(ctx context.Context, a *domain.Avatar) error {
	f.calls++
	f.lastAvatar = a
	if f.createFn != nil {
		return f.createFn(ctx, a)
	}
	return nil
}

func (f *fakeRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error) {
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return nil, domain.ErrAvatarNotFound
}

func (f *fakeRepo) ListByUserID(ctx context.Context, userID string, limit, offset int) ([]*domain.Avatar, error) {
	f.lastLimit = limit
	f.lastOffset = offset
	if f.listFn != nil {
		return f.listFn(ctx, userID)
	}
	return nil, nil
}

func (f *fakeRepo) GetLatestByUserID(ctx context.Context, userID string) (*domain.Avatar, error) {
	if f.latestFn != nil {
		return f.latestFn(ctx, userID)
	}
	return nil, domain.ErrAvatarNotFound
}

func (f *fakeRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	if f.softDeleteFn != nil {
		return f.softDeleteFn(ctx, id)
	}
	return nil
}

type fakeStorage struct {
	putFn    func(ctx context.Context, key, ct string, r io.Reader, size int64) error
	getFn    func(ctx context.Context, key string) (io.ReadCloser, int64, string, error)
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

func (f *fakeStorage) GetObject(ctx context.Context, key string) (io.ReadCloser, int64, string, error) {
	if f.getFn != nil {
		return f.getFn(ctx, key)
	}
	return io.NopCloser(bytes.NewReader(nil)), 0, "", nil
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

func TestAvatarService_Get_DelegatesToRepo(t *testing.T) {
	want := &domain.Avatar{ID: uuid.New(), UserID: "u1"}
	repo := &fakeRepo{getFn: func(ctx context.Context, _ uuid.UUID) (*domain.Avatar, error) {
		return want, nil
	}}
	svc := NewAvatarService(repo, &fakeStorage{}, &fakePublisher{})

	got, err := svc.Get(context.Background(), want.ID)
	require.NoError(t, err)
	require.Same(t, want, got)
}

func TestAvatarService_GetLatestForUser_ReturnsLatest(t *testing.T) {
	a := &domain.Avatar{ID: uuid.New(), UserID: "u1", FileName: "new.jpg"}
	repo := &fakeRepo{latestFn: func(ctx context.Context, _ string) (*domain.Avatar, error) {
		return a, nil
	}}
	svc := NewAvatarService(repo, &fakeStorage{}, &fakePublisher{})

	got, err := svc.GetLatestForUser(context.Background(), "u1")
	require.NoError(t, err)
	require.Same(t, a, got)
}

func TestAvatarService_GetLatestForUser_Empty(t *testing.T) {
	repo := &fakeRepo{latestFn: func(ctx context.Context, _ string) (*domain.Avatar, error) {
		return nil, domain.ErrAvatarNotFound
	}}
	svc := NewAvatarService(repo, &fakeStorage{}, &fakePublisher{})

	_, err := svc.GetLatestForUser(context.Background(), "u1")
	require.ErrorIs(t, err, domain.ErrAvatarNotFound)
}

func TestAvatarService_ListForUser_AppliesDefaultsAndClamps(t *testing.T) {
	cases := []struct {
		name          string
		limit, offset int
		wantLimit     int
		wantOffset    int
	}{
		{"zero limit -> default", 0, 0, DefaultListLimit, 0},
		{"negative limit -> default", -5, 0, DefaultListLimit, 0},
		{"huge limit clamped", 10000, 0, MaxListLimit, 0},
		{"negative offset -> 0", 10, -3, 10, 0},
		{"normal passthrough", 15, 40, 15, 40},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := &fakeRepo{}
			svc := NewAvatarService(repo, &fakeStorage{}, &fakePublisher{})
			_, err := svc.ListForUser(context.Background(), "u1", c.limit, c.offset)
			require.NoError(t, err)
			require.Equal(t, c.wantLimit, repo.lastLimit)
			require.Equal(t, c.wantOffset, repo.lastOffset)
		})
	}
}

func TestAvatarService_OpenContent_Original(t *testing.T) {
	avatar := &domain.Avatar{
		ID: uuid.New(), UserID: "u1", S3Key: "k", MimeType: "image/png",
	}
	body := []byte("data")
	repo := &fakeRepo{getFn: func(ctx context.Context, _ uuid.UUID) (*domain.Avatar, error) {
		return avatar, nil
	}}
	storage := &fakeStorage{getFn: func(ctx context.Context, key string) (io.ReadCloser, int64, string, error) {
		require.Equal(t, "k", key)
		return io.NopCloser(bytes.NewReader(body)), int64(len(body)), "image/jpeg", nil
	}}
	svc := NewAvatarService(repo, storage, &fakePublisher{})

	rc, sz, ct, err := svc.OpenContent(context.Background(), avatar.ID, "")
	require.NoError(t, err)
	defer rc.Close()
	require.Equal(t, int64(4), sz)
	require.Equal(t, "image/png", ct)
}

func TestAvatarService_OpenContent_Thumbnail(t *testing.T) {
	avatar := &domain.Avatar{
		ID: uuid.New(), UserID: "u1", S3Key: "orig",
		ThumbnailS3Keys: map[string]string{"100x100": "thumbs/100"},
	}
	repo := &fakeRepo{getFn: func(ctx context.Context, _ uuid.UUID) (*domain.Avatar, error) {
		return avatar, nil
	}}
	storage := &fakeStorage{getFn: func(ctx context.Context, key string) (io.ReadCloser, int64, string, error) {
		require.Equal(t, "thumbs/100", key)
		return io.NopCloser(bytes.NewReader([]byte("t"))), 1, "", nil
	}}
	svc := NewAvatarService(repo, storage, &fakePublisher{})

	rc, _, ct, err := svc.OpenContent(context.Background(), avatar.ID, "100x100")
	require.NoError(t, err)
	defer rc.Close()
	require.Equal(t, "image/jpeg", ct, "thumbnail content type must be jpeg")
}

func TestAvatarService_OpenContent_UnknownSize(t *testing.T) {
	repo := &fakeRepo{getFn: func(ctx context.Context, _ uuid.UUID) (*domain.Avatar, error) {
		return &domain.Avatar{ID: uuid.New()}, nil
	}}
	svc := NewAvatarService(repo, &fakeStorage{}, &fakePublisher{})

	_, _, _, err := svc.OpenContent(context.Background(), uuid.New(), "500x500")
	require.ErrorIs(t, err, ErrUnknownSize)
}

func TestAvatarService_Delete_RequiresOwnership(t *testing.T) {
	owner := "owner"
	other := "intruder"
	avatar := &domain.Avatar{ID: uuid.New(), UserID: owner, S3Key: "k"}

	repo := &fakeRepo{
		getFn: func(ctx context.Context, id uuid.UUID) (*domain.Avatar, error) {
			return avatar, nil
		},
	}
	storage := &fakeStorage{}
	svc := NewAvatarService(repo, storage, &fakePublisher{})

	err := svc.Delete(context.Background(), avatar.ID, other)
	require.ErrorIs(t, err, ErrForbidden)
	require.Equal(t, 0, storage.delCalls)
}

func TestAvatarService_Delete_RemovesAllObjects(t *testing.T) {
	owner := "owner"
	avatar := &domain.Avatar{
		ID:     uuid.New(),
		UserID: owner,
		S3Key:  "avatars/owner/main.jpg",
		ThumbnailS3Keys: map[string]string{
			"100x100": "thumbnails/x/100.jpg",
			"300x300": "thumbnails/x/300.jpg",
		},
	}
	softDeleted := false
	repo := &fakeRepo{
		getFn: func(ctx context.Context, id uuid.UUID) (*domain.Avatar, error) {
			return avatar, nil
		},
		softDeleteFn: func(ctx context.Context, id uuid.UUID) error {
			softDeleted = true
			return nil
		},
	}
	storage := &fakeStorage{}
	svc := NewAvatarService(repo, storage, &fakePublisher{})

	require.NoError(t, svc.Delete(context.Background(), avatar.ID, owner))
	require.True(t, softDeleted)
	require.Equal(t, 3, storage.delCalls, "original + 2 thumbnails")
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
