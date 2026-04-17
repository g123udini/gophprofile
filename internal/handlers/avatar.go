package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"gophprofile/internal/domain"
	"gophprofile/internal/service"
)

const (
	MaxFileSize = 10 * 1024 * 1024
	maxBodySize = MaxFileSize + 1024
)

type avatarService interface {
	Upload(ctx context.Context, in service.UploadInput) (*domain.Avatar, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	ListForUser(ctx context.Context, userID string, limit, offset int) ([]*domain.Avatar, error)
	GetLatestForUser(ctx context.Context, userID string) (*domain.Avatar, error)
	OpenContent(ctx context.Context, id uuid.UUID, size string) (io.ReadCloser, int64, string, error)
	Delete(ctx context.Context, id uuid.UUID, actingUserID string) error
}

type AvatarHandler struct {
	svc avatarService
}

func NewAvatarHandler(svc avatarService) *AvatarHandler {
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

func (h *AvatarHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid avatar id", err.Error())
		return
	}
	h.streamAvatar(w, r, id, r.URL.Query().Get("size"))
}

func (h *AvatarHandler) GetUserLatest(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "user_id")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "user_id required", "")
		return
	}
	avatar, err := h.svc.GetLatestForUser(r.Context(), userID)
	if err != nil {
		writeServiceError(w, err, "user latest avatar")
		return
	}
	h.streamAvatar(w, r, avatar.ID, r.URL.Query().Get("size"))
}

func (h *AvatarHandler) GetMetadata(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid avatar id", err.Error())
		return
	}
	avatar, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, err, "get metadata")
		return
	}
	writeJSON(w, http.StatusOK, toMetadata(avatar))
}

func (h *AvatarHandler) ListUserAvatars(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "user_id")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "user_id required", "")
		return
	}
	limit, err := parseNonNegativeIntQuery(r, "limit")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid limit", err.Error())
		return
	}
	offset, err := parseNonNegativeIntQuery(r, "offset")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid offset", err.Error())
		return
	}
	list, err := h.svc.ListForUser(r.Context(), userID, limit, offset)
	if err != nil {
		writeServiceError(w, err, "list user avatars")
		return
	}
	out := make([]metadataResponse, 0, len(list))
	for _, a := range list {
		out = append(out, toMetadata(a))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *AvatarHandler) Delete(w http.ResponseWriter, r *http.Request) {
	actingUserID := r.Header.Get("X-User-ID")
	if actingUserID == "" {
		writeError(w, http.StatusBadRequest, "X-User-ID header is required", "")
		return
	}
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid avatar id", err.Error())
		return
	}

	if err := h.svc.Delete(r.Context(), id, actingUserID); err != nil {
		switch {
		case errors.Is(err, domain.ErrAvatarNotFound):
			writeError(w, http.StatusNotFound, "Avatar not found", "")
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, "Forbidden",
				"You can only delete your own avatars")
		default:
			slog.Error("avatar delete failed", "err", err, "avatar_id", id)
			writeError(w, http.StatusInternalServerError, "Internal error", "")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AvatarHandler) streamAvatar(w http.ResponseWriter, r *http.Request, id uuid.UUID, size string) {
	rc, sz, contentType, err := h.svc.OpenContent(r.Context(), id, size)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrAvatarNotFound):
			writeError(w, http.StatusNotFound, "Avatar not found", "")
		case errors.Is(err, service.ErrUnknownSize):
			writeError(w, http.StatusBadRequest, "Unknown size",
				"supported: 100x100, 300x300, original")
		default:
			slog.Error("open avatar content failed", "err", err, "avatar_id", id)
			writeError(w, http.StatusInternalServerError, "Internal error", "")
		}
		return
	}
	defer rc.Close()

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if sz > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(sz, 10))
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if _, err := io.Copy(w, rc); err != nil {
		slog.Warn("stream avatar to client interrupted", "err", err)
	}
}

type metadataResponse struct {
	ID               string            `json:"id"`
	UserID           string            `json:"user_id"`
	FileName         string            `json:"file_name"`
	MimeType         string            `json:"mime_type"`
	Size             int64             `json:"size"`
	Thumbnails       map[string]string `json:"thumbnails,omitempty"`
	UploadStatus     string            `json:"upload_status"`
	ProcessingStatus string            `json:"processing_status"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

func toMetadata(a *domain.Avatar) metadataResponse {
	return metadataResponse{
		ID:               a.ID.String(),
		UserID:           a.UserID,
		FileName:         a.FileName,
		MimeType:         a.MimeType,
		Size:             a.SizeBytes,
		Thumbnails:       a.ThumbnailS3Keys,
		UploadStatus:     a.UploadStatus,
		ProcessingStatus: a.ProcessingStatus,
		CreatedAt:        a.CreatedAt,
		UpdatedAt:        a.UpdatedAt,
	}
}

func parseIDParam(r *http.Request) (uuid.UUID, error) {
	raw := chi.URLParam(r, "id")
	return uuid.Parse(raw)
}

// parseNonNegativeIntQuery reads an optional int query parameter. Missing or
// empty value returns 0 (service layer applies defaults). Non-numeric or
// negative values return an error.
func parseNonNegativeIntQuery(r *http.Request, key string) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must be non-negative", key)
	}
	return n, nil
}

func writeServiceError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, domain.ErrAvatarNotFound):
		writeError(w, http.StatusNotFound, "Avatar not found", "")
	default:
		slog.Error(op, "err", err)
		writeError(w, http.StatusInternalServerError, "Internal error", "")
	}
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
