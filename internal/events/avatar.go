package events

const (
	RoutingKeyAvatarUploaded = "avatar.uploaded"
)

type AvatarUploadedEvent struct {
	AvatarID string `json:"avatar_id"`
	UserID   string `json:"user_id"`
	S3Key    string `json:"s3_key"`
}
