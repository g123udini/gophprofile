package postgres_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"gophprofile/internal/domain"
	"gophprofile/internal/repository/postgres"
	"gophprofile/migrations"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ctr, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("gophprofile"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		log.Printf("start postgres container: %v", err)
		return 1
	}
	defer func() {
		_ = ctr.Terminate(context.Background())
	}()

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Printf("connection string: %v", err)
		return 1
	}

	if err := postgres.Migrate(dsn, migrations.FS); err != nil {
		log.Printf("migrate: %v", err)
		return 1
	}

	testPool, err = postgres.NewPool(ctx, dsn)
	if err != nil {
		log.Printf("pool: %v", err)
		return 1
	}
	defer testPool.Close()

	return m.Run()
}

func truncate(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), "TRUNCATE avatars")
	require.NoError(t, err)
}

func newAvatar(userID string) *domain.Avatar {
	return &domain.Avatar{
		UserID:           userID,
		FileName:         "me.jpg",
		MimeType:         "image/jpeg",
		SizeBytes:        12345,
		S3Key:            fmt.Sprintf("users/%s/me.jpg", userID),
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	}
}

func TestAvatarRepository_Create_FillsGeneratedFields(t *testing.T) {
	truncate(t)
	repo := postgres.NewAvatarRepository(testPool)

	a := newAvatar("u1")
	err := repo.Create(context.Background(), a)

	require.NoError(t, err)
	require.NotEqual(t, "00000000-0000-0000-0000-000000000000", a.ID.String(), "id must be generated")
	require.False(t, a.CreatedAt.IsZero())
	require.False(t, a.UpdatedAt.IsZero())
}

func TestAvatarRepository_GetByID_ReturnsCreatedRow(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	repo := postgres.NewAvatarRepository(testPool)

	a := newAvatar("u1")
	require.NoError(t, repo.Create(ctx, a))

	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, got.ID)
	require.Equal(t, a.UserID, got.UserID)
	require.Equal(t, a.FileName, got.FileName)
	require.Equal(t, a.MimeType, got.MimeType)
	require.Equal(t, a.SizeBytes, got.SizeBytes)
	require.Equal(t, a.S3Key, got.S3Key)
	require.Equal(t, domain.UploadStatusUploaded, got.UploadStatus)
	require.Equal(t, domain.ProcessingStatusPending, got.ProcessingStatus)
	require.Nil(t, got.ThumbnailS3Keys)
}

func TestAvatarRepository_GetByID_NotFoundReturnsDomainError(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	repo := postgres.NewAvatarRepository(testPool)

	a := newAvatar("u1")
	require.NoError(t, repo.Create(ctx, a))
	require.NoError(t, repo.SoftDelete(ctx, a.ID))

	_, err := repo.GetByID(ctx, a.ID)
	require.ErrorIs(t, err, domain.ErrAvatarNotFound)
}

func TestAvatarRepository_UpdateProcessing_StoresThumbnailsAndStatus(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	repo := postgres.NewAvatarRepository(testPool)

	a := newAvatar("u1")
	require.NoError(t, repo.Create(ctx, a))

	thumbs := map[string]string{
		"100x100": "thumbs/" + a.ID.String() + "/100.jpg",
		"300x300": "thumbs/" + a.ID.String() + "/300.jpg",
	}
	require.NoError(t, repo.UpdateProcessing(ctx, a.ID, domain.ProcessingStatusCompleted, thumbs))

	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ProcessingStatusCompleted, got.ProcessingStatus)
	require.Equal(t, thumbs, got.ThumbnailS3Keys)
	require.True(t, got.UpdatedAt.After(got.CreatedAt) || got.UpdatedAt.Equal(got.CreatedAt))
}

func TestAvatarRepository_ListByUserID_ScopesAndOrders(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	repo := postgres.NewAvatarRepository(testPool)

	older := newAvatar("u1")
	require.NoError(t, repo.Create(ctx, older))
	time.Sleep(5 * time.Millisecond)
	newer := newAvatar("u1")
	newer.FileName = "newer.jpg"
	require.NoError(t, repo.Create(ctx, newer))

	foreign := newAvatar("u2")
	require.NoError(t, repo.Create(ctx, foreign))

	list, err := repo.ListByUserID(ctx, "u1")
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, newer.ID, list[0].ID, "newest first")
	require.Equal(t, older.ID, list[1].ID)
}

func TestAvatarRepository_ListByUserID_HidesSoftDeleted(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	repo := postgres.NewAvatarRepository(testPool)

	kept := newAvatar("u1")
	require.NoError(t, repo.Create(ctx, kept))
	dead := newAvatar("u1")
	require.NoError(t, repo.Create(ctx, dead))
	require.NoError(t, repo.SoftDelete(ctx, dead.ID))

	list, err := repo.ListByUserID(ctx, "u1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, kept.ID, list[0].ID)
}

func TestAvatarRepository_SoftDelete_SecondCallReturnsNotFound(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	repo := postgres.NewAvatarRepository(testPool)

	a := newAvatar("u1")
	require.NoError(t, repo.Create(ctx, a))
	require.NoError(t, repo.SoftDelete(ctx, a.ID))
	require.ErrorIs(t, repo.SoftDelete(ctx, a.ID), domain.ErrAvatarNotFound)
}

func TestAvatarRepository_Create_AcceptsExternalID(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	repo := postgres.NewAvatarRepository(testPool)

	wantID := uuid.New()
	a := newAvatar("u1")
	a.ID = wantID
	require.NoError(t, repo.Create(ctx, a))
	require.Equal(t, wantID, a.ID)

	got, err := repo.GetByID(ctx, wantID)
	require.NoError(t, err)
	require.Equal(t, wantID, got.ID)
}

func TestAvatarRepository_UpdateProcessing_NotFound(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	repo := postgres.NewAvatarRepository(testPool)

	a := newAvatar("u1")
	require.NoError(t, repo.Create(ctx, a))
	require.NoError(t, repo.SoftDelete(ctx, a.ID))

	err := repo.UpdateProcessing(ctx, a.ID, domain.ProcessingStatusCompleted, nil)
	require.ErrorIs(t, err, domain.ErrAvatarNotFound)
}
