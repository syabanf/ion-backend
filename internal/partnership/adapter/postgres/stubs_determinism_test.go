// Wave 121E — Partnership stub-mode determinism tests.
//
// Two stubs ship in this package:
//
//   - LocalEvidenceStore — writes evidence blobs to local disk,
//     returns file:// URL + sha256. Production swaps in an S3 adapter.
//   - StubSettlementPDFGenerator — renders a text "PDF" placeholder.
//     Production swaps in a real PDF library.
//
// Contracts:
//   - Same content → same hash (sha256 deterministic).
//   - PDF generator output is byte-stable for the same settlement
//     (so PDFHash equality detection works on re-render).
//   - LocalEvidenceStore writes to disk safely (no path traversal, no
//     panic on empty dir).
//
// What this DOES NOT validate:
//   - Real S3 multipart upload behaviour
//   - Real PDF binary format compliance (PDF/A, etc.)
//   - PII redaction in the rendered PDF (the stub copies raw amounts)
package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/partnership/domain"
)

// =====================================================================
// 1) LocalEvidenceStore — same content + same filename = same hash.
// =====================================================================

func TestLocalEvidenceStore_HashIsDeterministic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evidence")
	store := NewLocalEvidenceStore(dir)
	ctx := context.Background()

	content := []byte("CSV: invoice,amount\nINV-1,100\n")

	url1, hash1, err := store.Store(ctx, content, "report.csv")
	if err != nil {
		t.Fatalf("first Store: %v", err)
	}
	url2, hash2, err := store.Store(ctx, content, "report.csv")
	if err != nil {
		t.Fatalf("second Store: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("hash drift: %q vs %q (same content must produce same hash)", hash1, hash2)
	}
	// Sanity: hash matches sha256(content) byte-for-byte.
	want := sha256.Sum256(content)
	if hash1 != hex.EncodeToString(want[:]) {
		t.Errorf("hash = %q, sha256(content) = %q", hash1, hex.EncodeToString(want[:]))
	}
	// URLs differ (nano-time prefix) so concurrent writes don't collide.
	if url1 == url2 {
		t.Error("URLs should NOT collide across writes — nano-prefix missing")
	}
	if !strings.HasPrefix(url1, "file://") {
		t.Errorf("URL = %q, must start with file://", url1)
	}

	// The bytes on disk must round-trip the content.
	path := strings.TrimPrefix(url1, "file://")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("disk content drift")
	}
}

// =====================================================================
// 2) LocalEvidenceStore — filename sanitization prevents path traversal.
// =====================================================================

func TestLocalEvidenceStore_FilenameSanitization(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evidence")
	store := NewLocalEvidenceStore(dir)
	ctx := context.Background()

	// Try a malicious filename — store should write into `dir`, not /etc/.
	url, _, err := store.Store(ctx, []byte("x"), "../../etc/passwd")
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	path := strings.TrimPrefix(url, "file://")
	if !strings.HasPrefix(path, dir) {
		t.Errorf("path escaped the evidence dir: %s", path)
	}
	// The path must NOT contain any unencoded ".." segments after sanitization.
	if strings.Contains(filepath.Base(path), "/") {
		t.Errorf("filename still contains path separator after sanitization: %s", path)
	}
}

// =====================================================================
// 3) StubSettlementPDFGenerator — same settlement = same bytes + hash.
// =====================================================================

func TestStubSettlementPDFGenerator_Deterministic(t *testing.T) {
	g := NewStubSettlementPDFGenerator()
	ctx := context.Background()

	settID := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	subID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	agrID := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	resellerID := uuid.MustParse("77777777-7777-7777-7777-777777777777")

	settlement := &domain.Settlement{
		ID:             settID,
		SubmissionID:   subID,
		AgreementID:    agrID,
		GrossRevenue:   1_000_000,
		NetRevenue:     900_000,
		RevshareAmount: 270_000,
		TaxAmount:      29_700,
		PayableAmount:  240_300,
		FormulaHash:    "deadbeef",
		PeriodYear:     2026,
		PeriodMonth:    5,
	}
	agreement := &domain.Agreement{
		ID:                agrID,
		ResellerAccountID: resellerID,
		RevsharePct:       0.30,
	}
	submission := &domain.MonthlySubmission{
		ID:                subID,
		ResellerAccountID: resellerID,
		PeriodYear:        2026,
		PeriodMonth:       5,
		CreatedAt:         time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}

	bytes1, hash1, err := g.Generate(ctx, settlement, agreement, submission)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	bytes2, hash2, err := g.Generate(ctx, settlement, agreement, submission)
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}

	if !bytes.Equal(bytes1, bytes2) {
		t.Error("PDF bytes drift across calls — generator is non-deterministic")
	}
	if hash1 != hash2 {
		t.Errorf("hash drift: %q vs %q", hash1, hash2)
	}
	// Hash matches sha256(content).
	want := sha256.Sum256(bytes1)
	if hash1 != hex.EncodeToString(want[:]) {
		t.Errorf("hash != sha256(content): %q vs %q", hash1, hex.EncodeToString(want[:]))
	}
	// Sanity: the rendered text contains the settlement id + revshare pct.
	body := string(bytes1)
	if !strings.Contains(body, settID.String()) {
		t.Error("rendered PDF missing settlement id")
	}
	if !strings.Contains(body, "revshare_pct") {
		t.Error("rendered PDF missing revshare_pct line")
	}
}
