package worker

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"io"
	"log/slog"

	"github.com/disintegration/imaging"
	"github.com/google/uuid"

	_ "golang.org/x/image/webp"

	"gophprofile/internal/domain"
	"gophprofile/internal/events"
)

var thumbnailSizes = []struct {
	Name string
	W, H int
}{
	{"100x100", 100, 100},
	{"300x300", 300, 300},
}

const jpegQuality = 85

type objectStore interface {
	GetObject(ctx context.Context, key string) (io.ReadCloser, int64, string, error)
	PutObject(ctx context.Context, key, contentType string, r io.Reader, size int64) error
}

type avatarRepo interface {
	UpdateProcessing(ctx context.Context, id uuid.UUID, status string, thumbs map[string]string) error
}

type AvatarProcessor struct {
	storage objectStore
	repo    avatarRepo
}

func NewAvatarProcessor(storage objectStore, repo avatarRepo) *AvatarProcessor {
	return &AvatarProcessor{storage: storage, repo: repo}
}

func (p *AvatarProcessor) HandleUploaded(ctx context.Context, evt events.AvatarUploadedEvent) error {
	avatarID, err := uuid.Parse(evt.AvatarID)
	if err != nil {
		return fmt.Errorf("parse avatar id %q: %w", evt.AvatarID, err)
	}

	rc, _, _, err := p.storage.GetObject(ctx, evt.S3Key)
	if err != nil {
		return fmt.Errorf("download original %q: %w", evt.S3Key, err)
	}
	defer rc.Close()

	src, err := imaging.Decode(rc, imaging.AutoOrientation(true))
	if err != nil {
		p.markFailed(ctx, avatarID, "decode image", err)
		return fmt.Errorf("decode image: %w", err)
	}

	thumbs := make(map[string]string, len(thumbnailSizes))
	for _, size := range thumbnailSizes {
		resized := imaging.Fill(src, size.W, size.H, imaging.Center, imaging.Lanczos)

		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: jpegQuality}); err != nil {
			p.markFailed(ctx, avatarID, "encode thumbnail", err)
			return fmt.Errorf("encode %s thumbnail: %w", size.Name, err)
		}

		key := fmt.Sprintf("thumbnails/%s/%s.jpg", avatarID.String(), size.Name)
		if err := p.storage.PutObject(ctx, key, "image/jpeg", bytes.NewReader(buf.Bytes()), int64(buf.Len())); err != nil {
			p.markFailed(ctx, avatarID, "upload thumbnail", err)
			return fmt.Errorf("upload %s thumbnail: %w", size.Name, err)
		}
		thumbs[size.Name] = key
	}

	if err := p.repo.UpdateProcessing(ctx, avatarID, domain.ProcessingStatusCompleted, thumbs); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}
	return nil
}

func (p *AvatarProcessor) markFailed(ctx context.Context, id uuid.UUID, stage string, cause error) {
	if err := p.repo.UpdateProcessing(context.Background(), id, domain.ProcessingStatusFailed, nil); err != nil {
		slog.Error("mark processing failed", "err", err, "stage", stage, "avatar_id", id, "cause", cause)
	}
}
