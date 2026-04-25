package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"github.com/google/uuid"

	"gophprofile/internal/domain"
	"gophprofile/internal/events"
)

var (
	ErrInvalidContentType = errors.New("invalid content type")
	ErrEmptyUserID        = errors.New("empty user id")
	ErrForbidden          = errors.New("forbidden")
	ErrUnknownSize        = errors.New("unknown size")
)

const (
	SizeOriginal = "original"
	Size100x100  = "100x100"
	Size300x300  = "300x300"
)

var allowedContentTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

type avatarRepo interface {
	Create(ctx context.Context, a *domain.Avatar) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	ListByUserID(ctx context.Context, userID string, limit, offset int) ([]*domain.Avatar, error)
	GetLatestByUserID(ctx context.Context, userID string) (*domain.Avatar, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

type avatarStorage interface {
	PutObject(ctx context.Context, key, contentType string, r io.Reader, size int64) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, int64, string, error)
	DeleteObject(ctx context.Context, key string) error
}

type eventPublisher interface {
	PublishAvatarUploaded(ctx context.Context, evt events.AvatarUploadedEvent) error
}

type AvatarService struct {
	repo      avatarRepo
	storage   avatarStorage
	publisher eventPublisher
}

func NewAvatarService(repo avatarRepo, storage avatarStorage, publisher eventPublisher) *AvatarService {
	return &AvatarService{repo: repo, storage: storage, publisher: publisher}
}

type UploadInput struct {
	UserID      string
	FileName    string
	ContentType string
	Size        int64
	Content     io.Reader
}

func (s *AvatarService) Upload(ctx context.Context, in UploadInput) (*domain.Avatar, error) {
	if in.UserID == "" {
		return nil, ErrEmptyUserID
	}
	ext, ok := allowedContentTypes[in.ContentType]
	if !ok {
		return nil, ErrInvalidContentType
	}

	id := uuid.New()
	key := buildKey(in.UserID, id, in.FileName, ext)

	if err := s.storage.PutObject(ctx, key, in.ContentType, in.Content, in.Size); err != nil {
		return nil, fmt.Errorf("upload: put object: %w", err)
	}

	avatar := &domain.Avatar{
		ID:               id,
		UserID:           in.UserID,
		FileName:         in.FileName,
		MimeType:         in.ContentType,
		SizeBytes:        in.Size,
		S3Key:            key,
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	}

	if err := s.repo.Create(ctx, avatar); err != nil {
		if delErr := s.storage.DeleteObject(context.Background(), key); delErr != nil {
			slog.ErrorContext(ctx, "rollback: delete orphaned s3 object",
				"key", key, "err", delErr, "original_err", err)
		}
		return nil, fmt.Errorf("upload: create avatar: %w", err)
	}

	// Broker failures leave the avatar in processing_status=pending forever.
	// Acceptable for the sprint-01 MVP; a proper fix is the outbox pattern
	// in sprint-03.
	if err := s.publisher.PublishAvatarUploaded(ctx, events.AvatarUploadedEvent{
		AvatarID: avatar.ID.String(),
		UserID:   avatar.UserID,
		S3Key:    avatar.S3Key,
	}); err != nil {
		slog.WarnContext(ctx, "publish avatar.uploaded failed; avatar will stay pending",
			"err", err, "avatar_id", avatar.ID)
	}

	return avatar, nil
}

func (s *AvatarService) Get(ctx context.Context, id uuid.UUID) (*domain.Avatar, error) {
	return s.repo.GetByID(ctx, id)
}

const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

func (s *AvatarService) ListForUser(ctx context.Context, userID string, limit, offset int) ([]*domain.Avatar, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.ListByUserID(ctx, userID, limit, offset)
}

func (s *AvatarService) GetLatestForUser(ctx context.Context, userID string) (*domain.Avatar, error) {
	return s.repo.GetLatestByUserID(ctx, userID)
}

// OpenContent returns a stream for the avatar's original or thumbnail. The
// caller must close the returned reader. size must be one of "original",
// "100x100", "300x300".
func (s *AvatarService) OpenContent(ctx context.Context, id uuid.UUID, size string) (io.ReadCloser, int64, string, error) {
	avatar, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, 0, "", err
	}

	key, contentType, err := resolveKey(avatar, size)
	if err != nil {
		return nil, 0, "", err
	}

	rc, sz, _, err := s.storage.GetObject(ctx, key)
	if err != nil {
		return nil, 0, "", err
	}
	return rc, sz, contentType, nil
}

func resolveKey(a *domain.Avatar, size string) (key, contentType string, err error) {
	switch size {
	case "", SizeOriginal:
		return a.S3Key, a.MimeType, nil
	case Size100x100, Size300x300:
		if a.ThumbnailS3Keys == nil {
			return "", "", domain.ErrAvatarNotFound
		}
		k, ok := a.ThumbnailS3Keys[size]
		if !ok {
			return "", "", domain.ErrAvatarNotFound
		}
		return k, "image/jpeg", nil
	default:
		return "", "", ErrUnknownSize
	}
}

// Delete soft-deletes the avatar and removes its S3 objects synchronously.
// Async deletion via RabbitMQ is a sprint-03 follow-up.
func (s *AvatarService) Delete(ctx context.Context, id uuid.UUID, actingUserID string) error {
	if actingUserID == "" {
		return ErrEmptyUserID
	}
	avatar, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if avatar.UserID != actingUserID {
		return ErrForbidden
	}

	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return err
	}

	// Best-effort cleanup of binary objects; errors only logged.
	if err := s.storage.DeleteObject(ctx, avatar.S3Key); err != nil {
		slog.WarnContext(ctx, "delete original from s3", "err", err, "key", avatar.S3Key)
	}
	for _, key := range avatar.ThumbnailS3Keys {
		if err := s.storage.DeleteObject(ctx, key); err != nil {
			slog.WarnContext(ctx, "delete thumbnail from s3", "err", err, "key", key)
		}
	}
	return nil
}

func buildKey(userID string, id uuid.UUID, fileName, fallbackExt string) string {
	ext := strings.ToLower(path.Ext(fileName))
	if ext == "" || !isKnownExt(ext) {
		ext = fallbackExt
	}
	return fmt.Sprintf("avatars/%s/%s%s", userID, id.String(), ext)
}

func isKnownExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	}
	return false
}
