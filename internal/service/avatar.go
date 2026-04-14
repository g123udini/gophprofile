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
)

var (
	ErrInvalidContentType = errors.New("invalid content type")
	ErrEmptyUserID        = errors.New("empty user id")
)

var allowedContentTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

type avatarRepo interface {
	Create(ctx context.Context, a *domain.Avatar) error
}

type avatarStorage interface {
	PutObject(ctx context.Context, key, contentType string, r io.Reader, size int64) error
	DeleteObject(ctx context.Context, key string) error
}

type AvatarService struct {
	repo    avatarRepo
	storage avatarStorage
}

func NewAvatarService(repo avatarRepo, storage avatarStorage) *AvatarService {
	return &AvatarService{repo: repo, storage: storage}
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
			slog.Error("rollback: delete orphaned s3 object",
				"key", key, "err", delErr, "original_err", err)
		}
		return nil, fmt.Errorf("upload: create avatar: %w", err)
	}

	return avatar, nil
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
