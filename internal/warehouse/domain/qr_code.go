// Wave 117 — QR code utilities. Deterministic encode + decode so the
// regenerated string equals the stored one (TC-IQR-* idempotency).
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ion-core/backend/pkg/errors"
)

// QRPayload is what a successful Parse returns.
type QRPayload struct {
	ItemType   ItemType
	ItemID     string // first 8 chars of the canonical UUID
	SerialHash string // first 12 chars of sha256(serial_number)
	Raw        string
}

// QRItemSource is the minimal contract a caller passes in to GenerateQR.
// Both StockItem and a partial-typed input satisfy this so the same path
// works for catalog regeneration + per-asset QR creation.
type QRItemSource interface {
	QRItemType() ItemType
	QRItemID() string
	QRSerialOrCode() string
}

// GenerateQR builds the canonical QR string for an item.
//
// Format: ION-{type}-{item_id_prefix}-{serial_hash}
//
//	type        = type1/type2/type3/type4
//	item_id_prefix = first 8 hex chars of the canonical item UUID
//	serial_hash  = first 12 hex chars of sha256(serial / code)
//
// The output is deterministic: same inputs → same string. Idempotent
// regeneration is a TC-IQR-* requirement.
func GenerateQR(src QRItemSource) string {
	t := src.QRItemType()
	if !t.Valid() {
		t = ItemTypeSerialized
	}
	id := strings.ReplaceAll(strings.ToLower(src.QRItemID()), "-", "")
	if len(id) > 8 {
		id = id[:8]
	}
	hash := sha256.Sum256([]byte(src.QRSerialOrCode()))
	serialHash := hex.EncodeToString(hash[:])[:12]
	return fmt.Sprintf("ION-%s-%s-%s", t, id, serialHash)
}

// ParseQR splits a QR string into its components. Returns an error if
// the string doesn't match the format.
func ParseQR(scanned string) (*QRPayload, error) {
	scanned = strings.TrimSpace(scanned)
	if !strings.HasPrefix(scanned, "ION-") {
		return nil, errors.Validation("qr.bad_prefix", "QR must start with ION-")
	}
	parts := strings.Split(scanned, "-")
	if len(parts) != 4 {
		return nil, errors.Validation("qr.bad_format", "QR format is ION-{type}-{id}-{hash}")
	}
	t := ItemType(parts[1])
	if !t.Valid() {
		return nil, errors.Validation("qr.bad_type", "QR type segment invalid")
	}
	if len(parts[2]) != 8 {
		return nil, errors.Validation("qr.bad_id", "QR id segment must be 8 chars")
	}
	if len(parts[3]) != 12 {
		return nil, errors.Validation("qr.bad_hash", "QR hash segment must be 12 chars")
	}
	return &QRPayload{
		ItemType:   t,
		ItemID:     parts[2],
		SerialHash: parts[3],
		Raw:        scanned,
	}, nil
}

// qrSource is a small helper so callers can build a payload inline
// without forcing every domain type to implement the interface.
type qrSource struct {
	t  ItemType
	id string
	sn string
}

func (s qrSource) QRItemType() ItemType    { return s.t }
func (s qrSource) QRItemID() string        { return s.id }
func (s qrSource) QRSerialOrCode() string  { return s.sn }

// NewQRSource is the inline-builder shortcut.
func NewQRSource(t ItemType, id, serialOrCode string) QRItemSource {
	return qrSource{t: t, id: id, sn: serialOrCode}
}
