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

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"gophprofile/internal/domain"
	"gophprofile/internal/handlers"
	"gophprofile/internal/service"
)

type fakeUploader struct {
	avatar *domain.Avatar
	err    error
	calls  int
	lastIn service.UploadInput
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

type drainingUploader struct {
	body []byte
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
