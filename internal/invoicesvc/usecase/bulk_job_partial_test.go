// Wave 120 — bulk job partial completion edge.
//
// Pins TC-IGE-* "if a bulk generation job has 3/5 items succeed and
// 2/5 fail, the final job status must be 'partial' (not 'completed'
// and not 'failed'). The dashboard then surfaces the failure list
// for the operator to retry."
//
// The existing TestBulkService_RunJobAllSuccess + RunJobAllFail tests
// in usecase_test.go cover the extremes. This test wires up a
// flaky generator (alternating success / failure) to exercise the
// middle.

package usecase

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
)

// flakyGenerator alternates: success, fail, success, fail, success...
// Use it to drive a job to a partial outcome.
type flakyGenerator struct {
	calls atomic.Int32
}

func (g *flakyGenerator) GenerateForCustomer(_ context.Context, _, _ uuid.UUID, _ port.GenerationKind) (*port.GeneratedInvoice, error) {
	n := g.calls.Add(1)
	if n%2 == 0 {
		return nil, errors.New("flaky: even-numbered call fails")
	}
	return &port.GeneratedInvoice{InvoiceID: uuid.New(), InvoiceNumber: "INV-FLAKY"}, nil
}

func TestBulkService_RunJob_PartialOnMixedResults(t *testing.T) {
	jobs := newStubBulkJobRepo()
	items := newStubBulkItemRepo()
	reader := newStubReader()
	// 5 customers — flakyGenerator yields odd-success/even-fail, so we
	// expect 3 successes (1, 3, 5) and 2 failures (2, 4).
	reader.bulkSet = []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	gen := &flakyGenerator{}

	svc := NewBulkService(jobs, items, reader, gen)
	job, err := svc.StartJob(context.Background(), port.StartBulkJobInput{Kind: domain.BulkJobMonthlyCycle})
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if job.TotalExpected != 5 {
		t.Fatalf("TotalExpected = %d, want 5", job.TotalExpected)
	}

	out, err := svc.RunJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if out.Status != domain.JobStatusPartial {
		t.Errorf("status = %s, want partial (mix of 3 succeed + 2 fail)", out.Status)
	}
	if out.TotalGenerated != 3 {
		t.Errorf("TotalGenerated = %d, want 3", out.TotalGenerated)
	}
	if out.TotalFailed != 2 {
		t.Errorf("TotalFailed = %d, want 2", out.TotalFailed)
	}
}
