package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	"github.com/ion-core/backend/pkg/audit"
	"github.com/ion-core/backend/pkg/errors"
)

// AlertWOService implements port.AlertWOUseCase — the TC-NAW-001
// "Create Maintenance WO from Alert" flow. The WorkOrderCreator port
// is injected in cmd/nocmon-svc/main.go; the default adapter is a
// stub that logs + returns a synthetic uuid, so the bounded context
// can ship before the field-WO service exposes a real endpoint.
type AlertWOService struct {
	faults  port.FaultEventRepository
	impacts port.FaultImpactRepository
	creator port.WorkOrderCreator
	audit   audit.Writer
}

func NewAlertWOService(
	faults port.FaultEventRepository,
	impacts port.FaultImpactRepository,
	creator port.WorkOrderCreator,
	w audit.Writer,
) *AlertWOService {
	if w == nil {
		w = audit.Nop{}
	}
	return &AlertWOService{faults: faults, impacts: impacts, creator: creator, audit: w}
}

var _ port.AlertWOUseCase = (*AlertWOService)(nil)

// ConvertFaultToWO is the TC-NAW-001/002/003 happy path:
//  1. Load the fault. Refuse if it's already linked to a WO (one
//     active maintenance WO per fault).
//  2. Resolve the impacted customer set so the field handler can
//     fan a customer notification on dispatch.
//  3. Call the WorkOrderCreator port. The returned WO id is the
//     bidirectional link (TC-NAW-003): we stamp ticket_wo_id here,
//     the field service stamps source_alert_id on its side.
//  4. Move the fault open → investigating (the WO creation acts as
//     both acknowledgement and start-of-work).
func (s *AlertWOService) ConvertFaultToWO(ctx context.Context, faultID, byUserID uuid.UUID) (*domain.FaultEvent, error) {
	if s.creator == nil {
		return nil, errors.New(errors.KindUnavailable, "alert_wo.no_creator", "work-order creator not configured")
	}
	f, err := s.faults.FindByID(ctx, faultID)
	if err != nil {
		return nil, err
	}
	if f.TicketWOID != nil {
		return nil, errors.Conflict("fault.wo_already_linked", "fault already has a linked work order")
	}

	impacted, err := s.impacts.ListForFault(ctx, faultID)
	if err != nil {
		return nil, err
	}
	custIDs := make([]uuid.UUID, 0, len(impacted))
	for _, i := range impacted {
		custIDs = append(custIDs, i.CustomerID)
	}

	woID, err := s.creator.CreateOutageWO(ctx, faultID, custIDs)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	prev := f.Status
	if err := f.Investigate(byUserID, now); err != nil {
		// The transition errored — but the WO is already created.
		// Stamp the id so a retry by the operator can see it; the
		// state stays whatever it was.
		f.TicketWOID = &woID
		f.UpdatedAt = now
		_ = s.faults.UpdateStatus(ctx, f)
		return nil, err
	}
	f.TicketWOID = &woID
	if err := s.faults.UpdateStatus(ctx, f); err != nil {
		return nil, err
	}

	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "nocmon",
		RecordType:   "nocmon.fault",
		RecordID:     f.ID.String(),
		FieldChanged: "ticket_wo_id",
		Before:       string(prev),
		After:        woID.String(),
		Reason:       "fault.wo_created by=" + byUserID.String(),
	})
	return f, nil
}

// StubWorkOrderCreator returns a synthetic uuid + logs nothing. Used
// by cmd/nocmon-svc/main.go as the default until the field-WO
// service exposes a service-to-service endpoint. Implementing
// port.WorkOrderCreator inline (rather than as a separate adapter
// package) keeps the wiring trivial.
type StubWorkOrderCreator struct{}

func (StubWorkOrderCreator) CreateOutageWO(_ context.Context, _ uuid.UUID, _ []uuid.UUID) (uuid.UUID, error) {
	return uuid.New(), nil
}

var _ port.WorkOrderCreator = StubWorkOrderCreator{}
