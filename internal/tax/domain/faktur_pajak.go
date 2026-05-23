package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// FakturStatus tracks the DJP submission lifecycle.
//
// State machine (enforced by TransitionTo):
//
//	Draft     ─► Submitted     (operator submits to DJP via SubmitFaktur)
//	Submitted ─► Approved      (DJP returns success)
//	Submitted ─► Rejected      (DJP returns rejection — terminal)
//	Approved  ─► Cancelled     (faktur batal — only path off Approved)
//	Rejected  : terminal
//	Cancelled : terminal
//
// Draft can also be Cancelled directly (operator gives up before
// submission); we accept Draft→Cancelled to keep the UI honest.
type FakturStatus string

const (
	FakturStatusDraft     FakturStatus = "draft"
	FakturStatusSubmitted FakturStatus = "submitted"
	FakturStatusApproved  FakturStatus = "approved"
	FakturStatusRejected  FakturStatus = "rejected"
	FakturStatusCancelled FakturStatus = "cancelled"
)

// JenisFaktur enumerates the DJP "jenis faktur" codes used on faktur
// pajak keluaran. Codes match the DJP e-Faktur spec.
type JenisFaktur string

const (
	// JenisFakturStandard — 01 Penyerahan kena PPN ke pihak lain.
	JenisFakturStandard JenisFaktur = "01"
	// JenisFakturBendaharawan — 02 Penyerahan ke pemungut Bendaharawan.
	JenisFakturBendaharawan JenisFaktur = "02"
	// JenisFakturPemungutLain — 03 Penyerahan ke pemungut PPN selain Bendaharawan.
	JenisFakturPemungutLain JenisFaktur = "03"
	// JenisFakturDPPNilaiLain — 04 Penyerahan dengan DPP Nilai Lain.
	JenisFakturDPPNilaiLain JenisFaktur = "04"
	// JenisFakturLainnya — 06 Penyerahan lainnya (turis asing, dsb.).
	JenisFakturLainnya JenisFaktur = "06"
	// JenisFakturFasilitas — 07 Penyerahan dengan fasilitas (tidak
	// dipungut / ditanggung pemerintah).
	JenisFakturFasilitas JenisFaktur = "07"
	// JenisFakturDibebaskan — 08 Penyerahan dibebaskan PPN.
	JenisFakturDibebaskan JenisFaktur = "08"
)

// allowedJenis is the lookup set used by validation.
var allowedJenis = map[JenisFaktur]struct{}{
	JenisFakturStandard:     {},
	JenisFakturBendaharawan: {},
	JenisFakturPemungutLain: {},
	JenisFakturDPPNilaiLain: {},
	JenisFakturLainnya:      {},
	JenisFakturFasilitas:    {},
	JenisFakturDibebaskan:   {},
}

// FakturPajak represents one outgoing faktur pajak record.
//
// `NomorSeri` is the DJP-issued serial; NULL/empty for drafts.
// `DJPResponsePayload` holds the raw DJP API response (jsonb) for
// audit + troubleshooting — we never inspect it as structured data
// from Go, just persist + display.
type FakturPajak struct {
	ID                 uuid.UUID
	InvoiceID          uuid.UUID
	SubsidiaryID       *uuid.UUID
	NomorSeri          string // empty for drafts
	JenisFaktur        JenisFaktur
	TanggalFaktur      *time.Time
	NPWPLawanTransaksi string
	DPP                float64
	PPN                float64
	Status             FakturStatus
	DJPResponsePayload []byte // raw jsonb; nil for drafts
	CreatedAt          time.Time
	UpdatedAt          time.Time

	// Wave 101 — chain integrity + cleaner DJP payload decoding.
	//
	// TaxSnapshotHash is copied from invoice.tax_snapshot_hash at draft
	// creation. Once submitted to DJP the hash is the proof-of-chain
	// — if the underlying tax_profile is replaced after issuance, the
	// hash stays put so the audit trail is preserved.
	//
	// DPPDecoded is the DJP-reported DPP from the response payload,
	// normalized to float64 so reconciliation queries don't have to
	// re-parse the jsonb. Nil for drafts that haven't been submitted.
	TaxSnapshotHash *string
	DPPDecoded      *float64
}

// NewDraftFaktur constructs a new Draft faktur for the given invoice.
// The status is forced to Draft regardless of input — the only way to
// progress is through TransitionTo / SubmitFaktur on the usecase.
func NewDraftFaktur(
	invoiceID, subsidiaryID uuid.UUID,
	jenis JenisFaktur,
	npwpLawan string,
	dpp, ppn float64,
) (*FakturPajak, error) {
	if invoiceID == uuid.Nil {
		return nil, errors.Validation(
			"faktur.invoice_required",
			"invoice_id is required",
		)
	}
	if jenis == "" {
		jenis = JenisFakturStandard
	}
	if _, ok := allowedJenis[jenis]; !ok {
		return nil, errors.Validation(
			"faktur.jenis_invalid",
			"jenis_faktur must be one of 01,02,03,04,06,07,08",
		)
	}
	if dpp < 0 {
		return nil, errors.Validation("faktur.dpp_negative", "dpp must be >= 0")
	}
	if ppn < 0 {
		return nil, errors.Validation("faktur.ppn_negative", "ppn must be >= 0")
	}
	now := time.Now().UTC()
	f := &FakturPajak{
		ID:                 uuid.New(),
		InvoiceID:          invoiceID,
		JenisFaktur:        jenis,
		NPWPLawanTransaksi: npwpLawan,
		DPP:                dpp,
		PPN:                ppn,
		Status:             FakturStatusDraft,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if subsidiaryID != uuid.Nil {
		sid := subsidiaryID
		f.SubsidiaryID = &sid
	}
	return f, nil
}

// TransitionTo applies the lifecycle state machine. Returns a
// validation error for illegal transitions so the HTTP layer surfaces
// 400 with a machine-readable code.
func (f *FakturPajak) TransitionTo(next FakturStatus) error {
	if f.Status == next {
		// Idempotent same-state transition.
		return nil
	}
	allowed := false
	switch f.Status {
	case FakturStatusDraft:
		allowed = next == FakturStatusSubmitted || next == FakturStatusCancelled
	case FakturStatusSubmitted:
		allowed = next == FakturStatusApproved || next == FakturStatusRejected
	case FakturStatusApproved:
		allowed = next == FakturStatusCancelled
	case FakturStatusRejected, FakturStatusCancelled:
		// Terminal.
		allowed = false
	}
	if !allowed {
		return errors.Conflict(
			"faktur.illegal_transition",
			"cannot transition faktur from "+string(f.Status)+" to "+string(next),
		)
	}
	f.Status = next
	f.UpdatedAt = time.Now().UTC()
	return nil
}

// MarkSubmitted records the DJP response after a successful
// IssueFaktur call. Sets the nomor_seri, persists the raw response,
// and flips the lifecycle to Submitted.
func (f *FakturPajak) MarkSubmitted(nomorSeri string, payload []byte) error {
	if err := f.TransitionTo(FakturStatusSubmitted); err != nil {
		return err
	}
	f.NomorSeri = nomorSeri
	f.DJPResponsePayload = payload
	if f.TanggalFaktur == nil {
		now := time.Now().UTC()
		f.TanggalFaktur = &now
	}
	f.UpdatedAt = time.Now().UTC()
	return nil
}
