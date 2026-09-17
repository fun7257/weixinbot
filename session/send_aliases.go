package session

import "context"

// SendImage is an alias for SendImageFile.
func (s *Session) SendImage(ctx context.Context, toUserID, filePath, caption string) error {
	return s.SendImageFile(ctx, toUserID, filePath, caption)
}

// SendVideo is an alias for SendVideoFile.
func (s *Session) SendVideo(ctx context.Context, toUserID, filePath, caption string) error {
	return s.SendVideoFile(ctx, toUserID, filePath, caption)
}

// SendFile is an alias for SendFileAttachment.
func (s *Session) SendFile(ctx context.Context, toUserID, filePath string) error {
	return s.SendFileAttachment(ctx, toUserID, filePath)
}

// SendVoiceFile is an alias for SendVoice.
func (s *Session) SendVoiceFile(ctx context.Context, toUserID, filePath string) error {
	return s.SendVoice(ctx, toUserID, filePath)
}
