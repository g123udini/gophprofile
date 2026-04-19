package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	UploadStatusUploading = "uploading"
	UploadStatusUploaded  = "uploaded"
	UploadStatusFailed    = "failed"
)

const (
	ProcessingStatusPending   = "pending"
	ProcessingStatusRunning   = "running"
	ProcessingStatusCompleted = "completed"
	ProcessingStatusFailed    = "failed"
)

type Avatar struct {
	ID               uuid.UUID
	UserID           string
	FileName         string
	MimeType         string
	SizeBytes        int64
	S3Key            string
	ThumbnailS3Keys  map[string]string
	UploadStatus     string
	ProcessingStatus string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

var ErrAvatarNotFound = errors.New("avatar not found")
