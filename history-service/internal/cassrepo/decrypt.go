package cassrepo

import (
	"context"
	"fmt"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/atrest"
)

// decryptIfNeeded decrypts m's enc_payload in place when present. When
// enc_payload is nil the row is treated as legacy plaintext and m is
// returned unchanged. When the cipher is nil (ATREST_ENABLED=false) and
// enc_payload is non-nil, a typed error is returned.
func (r *Repository) decryptIfNeeded(ctx context.Context, m *models.Message) error {
	if len(m.EncPayload) == 0 {
		return nil
	}
	if r.cipher == nil {
		return fmt.Errorf("encrypted row encountered but cipher is disabled (room=%s message=%s)", m.RoomID, m.MessageID)
	}
	meta := atrest.EncMeta{}
	if m.EncMeta != nil {
		meta.Nonce = m.EncMeta.Nonce
	}
	fields, err := r.cipher.Decrypt(ctx, m.RoomID, m.EncPayload, meta)
	if err != nil {
		return fmt.Errorf("decrypt message %s in room %s: %w", m.MessageID, m.RoomID, err)
	}
	atrest.ApplyDecryptedFields(m, &fields)
	// Clear the on-row encrypted fields so callers above the cassrepo
	// layer never see them.
	m.EncPayload = nil
	m.EncMeta = nil
	return nil
}
