package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"gophprofile/internal/domain"
	"gophprofile/internal/handlers"
	"gophprofile/internal/service"
)

type fakeUploader struct {
	avatar      *domain.Avatar
	err         error
	calls       int
	lastIn      service.UploadInput
	getFn       func(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	listFn      func(ctx context.Context, userID string) ([]*domain.Avatar, error)
	latestFn    func(ctx context.Context, userID string) (*domain.Avatar, error)
	openFn      func(ctx context.Context, id uuid.UUID, size string) (io.ReadCloser, int64, string, error)
	deleteFn    func(ctx context.Context, id uuid.UUID, actingUserID string) error
	deleteCalls int
	lastLimit   int
	lastOffset  int
}

func (f *fakeUploader) Upload(ctx context.Context, in service.UploadInput) (*domain.Avatar, error) {
	f.calls++
	f.lastIn = in
	if f.err != nil {
		return nil, f.err
	}
	if f.avatar != nil {
		return f.avatar, nil
	}
	return &domain.Avatar{
		ID:               uuid.New(),
		UserID:           in.UserID,
		FileName:         in.FileName,
		MimeType:         in.ContentType,
		SizeBytes:        in.Size,
		S3Key:            "avatars/" + in.UserID + "/mock",
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
		CreatedAt:        time.Now().UTC(),
	}, nil
}

func (f *fakeUploader) Get(ctx context.Context, id uuid.UUID) (*domain.Avatar, error) {
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return nil, domain.ErrAvatarNotFound
}

func (f *fakeUploader) ListForUser(ctx context.Context, userID string, limit, offset int) ([]*domain.Avatar, error) {
	f.lastLimit = limit
	f.lastOffset = offset
	if f.listFn != nil {
		return f.listFn(ctx, userID)
	}
	return nil, nil
}

func (f *fakeUploader) GetLatestForUser(ctx context.Context, userID string) (*domain.Avatar, error) {
	if f.latestFn != nil {
		return f.latestFn(ctx, userID)
	}
	return nil, domain.ErrAvatarNotFound
}

func (f *fakeUploader) OpenContent(ctx context.Context, id uuid.UUID, size string) (io.ReadCloser, int64, string, error) {
	if f.openFn != nil {
		return f.openFn(ctx, id, size)
	}
	return nil, 0, "", domain.ErrAvatarNotFound
}

func (f *fakeUploader) Delete(ctx context.Context, id uuid.UUID, actingUserID string) error {
	f.deleteCalls++
	if f.deleteFn != nil {
		return f.deleteFn(ctx, id, actingUserID)
	}
	return nil
}

func buildMultipart(t *testing.T, fieldName, filename, contentType string, body []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="`+fieldName+`"; filename="`+filename+`"`)
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	require.NoError(t, err)
	_, err = part.Write(body)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return &buf, mw.FormDataContentType()
}

func newRequest(t *testing.T, userID, fieldName, filename, contentType string, body []byte) *http.Request {
	t.Helper()
	buf, ct := buildMultipart(t, fieldName, filename, contentType, body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", buf)
	req.Header.Set("Content-Type", ct)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	return req
}

func TestAvatarHandler_Upload_HappyPath_ImageField(t *testing.T) {
	fake := &fakeUploader{}
	h := handlers.NewAvatarHandler(fake)

	req := newRequest(t, "user-1", "image", "me.jpg", "image/jpeg", []byte("bytes"))
	rec := httptest.NewRecorder()
	h.Upload(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, 1, fake.calls)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.NotEmpty(t, resp["id"])
	require.Equal(t, "user-1", resp["user_id"])
	require.Equal(t, "processing", resp["status"])
	require.Contains(t, resp["url"], "/api/v1/avatars/")

	require.Equal(t, "user-1", fake.lastIn.UserID)
	require.Equal(t, "me.jpg", fake.lastIn.FileName)
	require.Equal(t, "image/jpeg", fake.lastIn.ContentType)
	require.Equal(t, int64(5), fake.lastIn.Size)
}

func TestAvatarHandler_Upload_HappyPath_FileField(t *testing.T) {
	fake := &fakeUploader{}
	h := handlers.NewAvatarHandler(fake)

	req := newRequest(t, "user-1", "file", "me.png", "image/png", []byte("png-bytes"))
	rec := httptest.NewRecorder()
	h.Upload(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, 1, fake.calls)
}

func TestAvatarHandler_Upload_MissingUserID(t *testing.T) {
	fake := &fakeUploader{}
	h := handlers.NewAvatarHandler(fake)

	req := newRequest(t, "", "image", "me.jpg", "image/jpeg", []byte("x"))
	rec := httptest.NewRecorder()
	h.Upload(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, 0, fake.calls)
	require.Contains(t, rec.Body.String(), "X-User-ID")
}

func TestAvatarHandler_Upload_UnknownFieldName(t *testing.T) {
	fake := &fakeUploader{}
	h := handlers.NewAvatarHandler(fake)

	req := newRequest(t, "user-1", "something-else", "me.jpg", "image/jpeg", []byte("x"))
	rec := httptest.NewRecorder()
	h.Upload(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, 0, fake.calls)
	require.Contains(t, rec.Body.String(), "file")
}

func TestAvatarHandler_Upload_ServiceRejectsContentType(t *testing.T) {
	fake := &fakeUploader{err: service.ErrInvalidContentType}
	h := handlers.NewAvatarHandler(fake)

	req := newRequest(t, "user-1", "image", "doc.pdf", "application/pdf", []byte("x"))
	rec := httptest.NewRecorder()
	h.Upload(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid file format")
}

func TestAvatarHandler_Upload_BodyExceedsLimit(t *testing.T) {
	fake := &fakeUploader{}
	h := handlers.NewAvatarHandler(fake)

	big := bytes.Repeat([]byte("A"), handlers.MaxFileSize+2048)
	req := newRequest(t, "user-1", "image", "big.jpg", "image/jpeg", big)
	rec := httptest.NewRecorder()
	h.Upload(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Equal(t, 0, fake.calls)
}

func TestAvatarHandler_Upload_ServiceInternalError(t *testing.T) {
	fake := &fakeUploader{err: errors.New("db down")}
	h := handlers.NewAvatarHandler(fake)

	req := newRequest(t, "user-1", "image", "me.jpg", "image/jpeg", []byte("x"))
	rec := httptest.NewRecorder()
	h.Upload(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestAvatarHandler_Upload_PassesFullBodyToService(t *testing.T) {
	drain := &drainingUploader{}
	h := handlers.NewAvatarHandler(drain)

	payload := bytes.Repeat([]byte("Z"), 2048)
	req := newRequest(t, "user-1", "image", "me.jpg", "image/jpeg", payload)
	rec := httptest.NewRecorder()
	h.Upload(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, payload, drain.body)
}

// -----------------------------------------------------------------
// Tests for the remaining read/delete endpoints.

func newRouter(h *handlers.AvatarHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/v1/avatars/{id}", h.GetByID)
	r.Get("/api/v1/avatars/{id}/metadata", h.GetMetadata)
	r.Delete("/api/v1/avatars/{id}", h.Delete)
	r.Get("/api/v1/users/{user_id}/avatar", h.GetUserLatest)
	r.Get("/api/v1/users/{user_id}/avatars", h.ListUserAvatars)
	return r
}

func TestAvatarHandler_GetByID_StreamsOriginal(t *testing.T) {
	id := uuid.New()
	body := []byte("image-bytes")
	fake := &fakeUploader{
		openFn: func(ctx context.Context, gotID uuid.UUID, size string) (io.ReadCloser, int64, string, error) {
			require.Equal(t, id, gotID)
			require.Equal(t, "", size)
			return io.NopCloser(bytes.NewReader(body)), int64(len(body)), "image/jpeg", nil
		},
	}
	h := handlers.NewAvatarHandler(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil)
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
	require.Equal(t, body, rec.Body.Bytes())
}

func TestAvatarHandler_GetByID_NotFound(t *testing.T) {
	id := uuid.New()
	fake := &fakeUploader{
		openFn: func(ctx context.Context, _ uuid.UUID, _ string) (io.ReadCloser, int64, string, error) {
			return nil, 0, "", domain.ErrAvatarNotFound
		},
	}
	h := handlers.NewAvatarHandler(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil)
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAvatarHandler_GetMetadata_ReturnsJSON(t *testing.T) {
	id := uuid.New()
	fake := &fakeUploader{
		getFn: func(ctx context.Context, _ uuid.UUID) (*domain.Avatar, error) {
			return &domain.Avatar{
				ID: id, UserID: "u", FileName: "me.jpg", MimeType: "image/jpeg",
				SizeBytes: 42, S3Key: "k", UploadStatus: "uploaded", ProcessingStatus: "completed",
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
				ThumbnailS3Keys: map[string]string{"100x100": "thumb-100"},
			}, nil
		},
	}
	h := handlers.NewAvatarHandler(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String()+"/metadata", nil)
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, id.String(), resp["id"])
	require.Equal(t, "completed", resp["processing_status"])
	thumbs := resp["thumbnails"].(map[string]any)
	require.Equal(t, "thumb-100", thumbs["100x100"])
}

func TestAvatarHandler_ListUserAvatars_ReturnsArray(t *testing.T) {
	fake := &fakeUploader{
		listFn: func(ctx context.Context, userID string) ([]*domain.Avatar, error) {
			require.Equal(t, "u1", userID)
			return []*domain.Avatar{
				{ID: uuid.New(), UserID: "u1", FileName: "a.jpg"},
				{ID: uuid.New(), UserID: "u1", FileName: "b.jpg"},
			}, nil
		},
	}
	h := handlers.NewAvatarHandler(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/u1/avatars", nil)
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var list []map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&list))
	require.Len(t, list, 2)
}

func TestAvatarHandler_ListUserAvatars_PassesPagination(t *testing.T) {
	fake := &fakeUploader{
		listFn: func(ctx context.Context, _ string) ([]*domain.Avatar, error) {
			return nil, nil
		},
	}
	h := handlers.NewAvatarHandler(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/u1/avatars?limit=5&offset=10", nil)
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 5, fake.lastLimit)
	require.Equal(t, 10, fake.lastOffset)
}

func TestAvatarHandler_ListUserAvatars_InvalidPaginationRejected(t *testing.T) {
	cases := []string{
		"?limit=abc",
		"?offset=-1",
		"?limit=-5",
	}
	for _, qs := range cases {
		t.Run(qs, func(t *testing.T) {
			fake := &fakeUploader{}
			h := handlers.NewAvatarHandler(fake)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/users/u1/avatars"+qs, nil)
			rec := httptest.NewRecorder()
			newRouter(h).ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestAvatarHandler_Delete_NoUserID(t *testing.T) {
	id := uuid.New()
	fake := &fakeUploader{}
	h := handlers.NewAvatarHandler(fake)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/"+id.String(), nil)
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, 0, fake.deleteCalls)
}

func TestAvatarHandler_Delete_Forbidden(t *testing.T) {
	id := uuid.New()
	fake := &fakeUploader{
		deleteFn: func(ctx context.Context, _ uuid.UUID, _ string) error {
			return service.ErrForbidden
		},
	}
	h := handlers.NewAvatarHandler(fake)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/"+id.String(), nil)
	req.Header.Set("X-User-ID", "intruder")
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestAvatarHandler_Delete_Success(t *testing.T) {
	id := uuid.New()
	fake := &fakeUploader{}
	h := handlers.NewAvatarHandler(fake)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/"+id.String(), nil)
	req.Header.Set("X-User-ID", "owner")
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, fake.deleteCalls)
}

type drainingUploader struct {
	body []byte
	fakeUploader
}

func (d *drainingUploader) Upload(ctx context.Context, in service.UploadInput) (*domain.Avatar, error) {
	b, err := io.ReadAll(in.Content)
	if err != nil {
		return nil, err
	}
	d.body = b
	return &domain.Avatar{
		ID:               uuid.New(),
		UserID:           in.UserID,
		FileName:         in.FileName,
		MimeType:         in.ContentType,
		SizeBytes:        in.Size,
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
		CreatedAt:        time.Now().UTC(),
	}, nil
}
