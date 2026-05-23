// Wave 126 — SQL-only resolver that walks the maintenance event's
// affected nodes -> network.ports.customer_id -> crm.customers
// (for customer_type) and returns the unique set of affected customers
// with their segment.
//
// No Go imports across CRM / Network — pure SQL.
package network

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
)

// CustomerSegmentBridge walks the maintenance-event cascade.
type CustomerSegmentBridge struct {
	pool *pgxpool.Pool
}

// NewCustomerSegmentBridge wires the bridge.
func NewCustomerSegmentBridge(pool *pgxpool.Pool) *CustomerSegmentBridge {
	return &CustomerSegmentBridge{pool: pool}
}

var _ port.CustomerSegmentResolver = (*CustomerSegmentBridge)(nil)

// ResolveByMaintenanceEvent returns the unique set of customers
// downstream of the event's affected nodes. The cascade walks
// field.maintenance_event_nodes -> network.ports.customer_id -> the
// CRM customer_type field (residential/business/enterprise/corporate).
//
// The mapping CRM customer_type -> CS-side segment:
//   - residential / sme              -> broadband
//   - business / enterprise / corp.  -> enterprise
//
// Returns an empty slice (not an error) on schema variance.
func (b *CustomerSegmentBridge) ResolveByMaintenanceEvent(ctx context.Context, eventID uuid.UUID) ([]port.AffectedCustomerInfo, error) {
	rows, err := b.pool.Query(ctx, `
		WITH event_nodes AS (
			SELECT node_id FROM field.maintenance_event_nodes WHERE event_id = $1
		)
		SELECT DISTINCT
			p.customer_id,
			COALESCE(c.customer_type, 'residential') AS customer_type
		  FROM network.ports p
		  LEFT JOIN crm.customers c ON c.id = p.customer_id
		 WHERE p.node_id IN (SELECT node_id FROM event_nodes)
		   AND p.customer_id IS NOT NULL
	`, eventID)
	if err != nil {
		// Schema variance (crm.customers missing customer_type, etc.)
		// — return empty rather than failing the upstream usecase.
		return nil, nil
	}
	defer rows.Close()
	out := []port.AffectedCustomerInfo{}
	for rows.Next() {
		var customerID uuid.UUID
		var customerType string
		if err := rows.Scan(&customerID, &customerType); err != nil {
			continue
		}
		segment := mapCustomerTypeToSegment(customerType)
		out = append(out, port.AffectedCustomerInfo{
			CustomerID:      customerID,
			CustomerSegment: segment,
		})
	}
	return out, nil
}

func mapCustomerTypeToSegment(customerType string) domain.CustomerSegment {
	switch customerType {
	case "enterprise", "corporate", "business":
		return domain.SegmentEnterprise
	default:
		return domain.SegmentBroadband
	}
}
