package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"time"

	"gophprofile/internal/domain"
	"gophprofile/internal/service"
)

const (
	MaxFileSize = 10 * 1024 * 1024
	maxBodySize = MaxFileSize + 1024
)

type avatarUploader interface {
	Upload(ctx context.Context, in service.UploadInput) (*domain.Avatar, error)
}

type AvatarHandler struct {
	svc avatarUploader
}

func NewAvatarHandler(svc avatarUploader) *AvatarHandler {
	return &AvatarHandler{svc: svc}
}

type uploadResponse struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	URL       string    `json:"url"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func (h *AvatarHandler) Upload(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "X-User-ID header is required", "")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "File too large",
				fmt.Sprintf("max size is %d bytes", MaxFileSize))
			return
		}
		writeError(w, http.StatusBadRequest, "Invalid multipart form", err.Error())
		return
	}

	file, header, err := openFormFile(r, "image", "file")
	if err != nil {
		writeError(w, http.StatusBadRequest,
			"No file uploaded",
			`expected form field "image" or "file"`)
		return
	}
	defer file.Close()

	if header.Size > MaxFileSize {
		writeError(w, http.StatusRequestEntityTooLarge, "File too large",
			fmt.Sprintf("max size is %d bytes", MaxFileSize))
		return
	}

	contentType := header.Header.Get("Content-Type")

	avatar, err := h.svc.Upload(r.Context(), service.UploadInput{
		UserID:      userID,
		FileName:    header.Filename,
		ContentType: contentType,
		Size:        header.Size,
		Content:     file,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidContentType):
			writeError(w, http.StatusBadRequest, "Invalid file format",
				"Supported formats: jpeg, png, webp")
		case errors.Is(err, service.ErrEmptyUserID):
			writeError(w, http.StatusBadRequest, "X-User-ID header is required", "")
		default:
			slog.Error("avatar upload failed", "err", err, "user_id", userID)
			writeError(w, http.StatusInternalServerError, "Internal error", "")
		}
		return
	}

	writeJSON(w, http.StatusCreated, uploadResponse{
		ID:        avatar.ID.String(),
		UserID:    avatar.UserID,
		URL:       "/api/v1/avatars/" + avatar.ID.String(),
		Status:    mapStatus(avatar.ProcessingStatus),
		CreatedAt: avatar.CreatedAt,
	})
}

func openFormFile(r *http.Request, fields ...string) (multipart.File, *multipart.FileHeader, error) {
	for _, f := range fields {
		file, header, err := r.FormFile(f)
		if err == nil {
			return file, header, nil
		}
	}
	return nil, nil, http.ErrMissingFile
}

func mapStatus(s string) string {
	switch s {
	case domain.ProcessingStatusPending, domain.ProcessingStatusRunning:
		return "processing"
	case domain.ProcessingStatusCompleted:
		return "ready"
	case domain.ProcessingStatusFailed:
		return "failed"
	default:
		return s
	}
}

type errorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

func writeError(w http.ResponseWriter, code int, msg, details string) {
	writeJSON(w, code, errorResponse{Error: msg, Details: details})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
