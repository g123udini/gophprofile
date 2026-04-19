package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gophprofile/internal/domain"
)

type AvatarRepository struct {
	pool *pgxpool.Pool
}

func NewAvatarRepository(pool *pgxpool.Pool) *AvatarRepository {
	return &AvatarRepository{pool: pool}
}

const avatarColumns = `id, user_id, file_name, mime_type, size_bytes, s3_key, thumbnail_s3_keys,
       upload_status, processing_status, created_at, updated_at`

func (r *AvatarRepository) Create(ctx context.Context, a *domain.Avatar) error {
	const q = `
INSERT INTO avatars (id, user_id, file_name, mime_type, size_bytes, s3_key, upload_status, processing_status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING created_at, updated_at`
	err := r.pool.QueryRow(ctx, q,
		a.ID, a.UserID, a.FileName, a.MimeType, a.SizeBytes, a.S3Key,
		a.UploadStatus, a.ProcessingStatus,
	).Scan(&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create avatar: %w", err)
	}
	return nil
}

func (r *AvatarRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error) {
	q := `SELECT ` + avatarColumns + ` FROM avatars WHERE id = $1 AND deleted_at IS NULL`
	return scanAvatar(r.pool.QueryRow(ctx, q, id))
}

func (r *AvatarRepository) ListByUserID(ctx context.Context, userID string, limit, offset int) ([]*domain.Avatar, error) {
	q := `SELECT ` + avatarColumns + ` FROM avatars
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT $2 OFFSET $3`
	rows, err := r.pool.Query(ctx, q, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list avatars: %w", err)
	}
	defer rows.Close()

	var out []*domain.Avatar
	for rows.Next() {
		a, err := scanAvatar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list avatars: %w", err)
	}
	return out, nil
}

func (r *AvatarRepository) GetLatestByUserID(ctx context.Context, userID string) (*domain.Avatar, error) {
	q := `SELECT ` + avatarColumns + ` FROM avatars
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 1`
	return scanAvatar(r.pool.QueryRow(ctx, q, userID))
}

func (r *AvatarRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE avatars SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL`
	ct, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("soft delete avatar: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrAvatarNotFound
	}
	return nil
}

func (r *AvatarRepository) GetProcessingStatus(ctx context.Context, id uuid.UUID) (string, error) {
	const q = `SELECT processing_status FROM avatars WHERE id = $1 AND deleted_at IS NULL`
	var status string
	err := r.pool.QueryRow(ctx, q, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrAvatarNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get processing status: %w", err)
	}
	return status, nil
}

func (r *AvatarRepository) UpdateProcessing(ctx context.Context, id uuid.UUID, status string, thumbs map[string]string) error {
	var thumbsJSON []byte
	if thumbs != nil {
		var err error
		thumbsJSON, err = json.Marshal(thumbs)
		if err != nil {
			return fmt.Errorf("marshal thumbnails: %w", err)
		}
	}

	const q = `UPDATE avatars
SET processing_status = $2, thumbnail_s3_keys = $3, updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL`
	ct, err := r.pool.Exec(ctx, q, id, status, thumbsJSON)
	if err != nil {
		return fmt.Errorf("update processing: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrAvatarNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAvatar(s scanner) (*domain.Avatar, error) {
	a := &domain.Avatar{}
	var thumbs []byte
	err := s.Scan(
		&a.ID, &a.UserID, &a.FileName, &a.MimeType, &a.SizeBytes, &a.S3Key, &thumbs,
		&a.UploadStatus, &a.ProcessingStatus, &a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAvatarNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan avatar: %w", err)
	}
	if len(thumbs) > 0 {
		if err := json.Unmarshal(thumbs, &a.ThumbnailS3Keys); err != nil {
			return nil, fmt.Errorf("unmarshal thumbnails: %w", err)
		}
	}
	return a, nil
}
