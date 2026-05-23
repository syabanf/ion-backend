package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ion-core/backend/internal/partnership/domain"
	"github.com/ion-core/backend/internal/partnership/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// LocalEvidenceStore — writes evidence blobs to /tmp + returns sha256.
//
// Wave 100 ships this stub so the submission flow works end-to-end
// without S3 wiring. Wave 100b can swap in an S3 adapter without
// touching the usecase — the port.EvidenceStore interface is what
// IssueSettlementForSubmission depends on, not this concrete type.
// =====================================================================

type LocalEvidenceStore struct {
	dir string
}

// NewLocalEvidenceStore writes to `dir` (created on first use).
// Pass "" to default to os.TempDir() + "/ion-partnership-evidence".
func NewLocalEvidenceStore(dir string) *LocalEvidenceStore {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(os.TempDir(), "ion-partnership-evidence")
	}
	return &LocalEvidenceStore{dir: dir}
}

var _ port.EvidenceStore = (*LocalEvidenceStore)(nil)

// Store writes content to disk and returns a file:// URL + sha256 hex.
// The filename is namespaced with a unix-nano prefix so concurrent
// callers don't collide on the same source filename.
func (s *LocalEvidenceStore) Store(ctx context.Context, content []byte, filename string) (string, string, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", "", derrors.Wrap(derrors.KindInternal, "evidence.mkdir", "create evidence dir", err)
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	safe := sanitizeFilename(filename)
	name := fmt.Sprintf("%d_%s_%s", time.Now().UnixNano(), hash[:8], safe)
	path := filepath.Join(s.dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", "", derrors.Wrap(derrors.KindInternal, "evidence.write", "write evidence file", err)
	}
	return "file://" + path, hash, nil
}

func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "evidence.bin"
	}
	// Strip path separators so an attacker can't write outside the dir.
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// =====================================================================
// StubSettlementPDFGenerator — renders a plain-text "PDF" placeholder.
//
// The real implementation will use a PDF library (gofpdf, pdfcpu,
// chromium-headless, etc.) in a follow-up. This stub returns a
// human-readable text byte stream so the smoke test can verify the
// generator wiring + storage hash without pulling a heavyweight
// dependency. The hash is sha256(content) so the formula-hash flow is
// exercised end-to-end.
// =====================================================================

type StubSettlementPDFGenerator struct{}

func NewStubSettlementPDFGenerator() *StubSettlementPDFGenerator {
	return &StubSettlementPDFGenerator{}
}

var _ port.SettlementPDFGenerator = (*StubSettlementPDFGenerator)(nil)

func (g *StubSettlementPDFGenerator) Generate(
	ctx context.Context,
	settlement *domain.Settlement,
	agreement *domain.Agreement,
	submission *domain.MonthlySubmission,
) ([]byte, string, error) {
	var b strings.Builder
	b.WriteString("=== ION Partnership Settlement (Wave 100 stub PDF) ===\n")
	b.WriteString(fmt.Sprintf("settlement_id: %s\n", settlement.ID.String()))
	b.WriteString(fmt.Sprintf("submission_id: %s\n", settlement.SubmissionID.String()))
	b.WriteString(fmt.Sprintf("agreement_id:  %s\n", settlement.AgreementID.String()))
	if submission != nil {
		b.WriteString(fmt.Sprintf("period:        %04d-%02d\n", submission.PeriodYear, submission.PeriodMonth))
		b.WriteString(fmt.Sprintf("reseller_id:   %s\n", submission.ResellerAccountID.String()))
	}
	if agreement != nil {
		b.WriteString(fmt.Sprintf("revshare_pct:  %.4f\n", agreement.RevsharePct))
	}
	b.WriteString("\n--- Formula breakdown ---\n")
	b.WriteString(fmt.Sprintf("gross_revenue:   %.2f\n", settlement.GrossRevenue))
	b.WriteString(fmt.Sprintf("net_revenue:     %.2f\n", settlement.NetRevenue))
	b.WriteString(fmt.Sprintf("revshare_amount: %.2f\n", settlement.RevshareAmount))
	b.WriteString(fmt.Sprintf("tax_amount:      %.2f\n", settlement.TaxAmount))
	b.WriteString(fmt.Sprintf("payable_amount:  %.2f\n", settlement.PayableAmount))
	b.WriteString(fmt.Sprintf("formula_hash:    %s\n", settlement.FormulaHash))
	content := []byte(b.String())
	sum := sha256.Sum256(content)
	return content, hex.EncodeToString(sum[:]), nil
}
