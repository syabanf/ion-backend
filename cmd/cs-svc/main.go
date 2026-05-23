// cs-svc — Customer Service bounded context service.
//
// Wave 123 scope: ~26 TCs across Ticket Types, Ticket Lifecycle,
// Ticket Channels, and @Mentions foundation. Owns the cs.* schema.
// Cross-context bridges (mention resolution against identity.users,
// notifyx fan-out) are tiny inline SQL adapters declared in this file
// — they keep internal/cs/ free of cross-context imports while still
// letting the comment service ping mentioned users via notifyx.
//
// Wave 124 lights up the SLA matrix + Service Requests + Teams + WO-from-Ticket
// + CSAT + Communications surface. The new bridges (customerTypeResolverBridge,
// woFromTicketBridge, csatInviteDispatcherBridge) live in this file so
// internal/cs/ remains free of cross-context imports.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	cshttp "github.com/ion-core/backend/internal/cs/adapter/http"
	cspg "github.com/ion-core/backend/internal/cs/adapter/postgres"
	cscron "github.com/ion-core/backend/internal/cs/cron"
	"github.com/ion-core/backend/internal/cs/domain"
	csport "github.com/ion-core/backend/internal/cs/port"
	csusecase "github.com/ion-core/backend/internal/cs/usecase"
	opspg "github.com/ion-core/backend/internal/operations/adapter/postgres"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/config"
	"github.com/ion-core/backend/pkg/database"
	derrors "github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/logger"
	"github.com/ion-core/backend/pkg/notifyx"
)

func main() {
	cfg, err := config.Load("CS_SVC_PORT")
	if err != nil {
		println("config error:", err.Error())
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With("service", "cs-svc")
	log.Info("starting cs-svc", "port", cfg.HTTPPort)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(cfg.DatabaseURL))
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Repositories
	ticketRepo := cspg.NewTicketRepository(pool)
	eventRepo := cspg.NewTicketEventRepository(pool)
	commentRepo := cspg.NewTicketCommentRepository(pool)
	mentionRepo := cspg.NewTicketMentionRepository(pool)
	channelRepo := cspg.NewTicketChannelRepository(pool)

	// Wave 124 repositories
	slaMatrixRepo := cspg.NewSLAMatrixRepository(pool)
	srRepo := cspg.NewServiceRequestRepository(pool)
	teamRepo := cspg.NewTeamRepository(pool)
	teamMemberRepo := cspg.NewTeamMemberRepository(pool)
	assignHistRepo := cspg.NewAssignmentHistoryRepository(pool)
	csatRepo := cspg.NewCSATRepository(pool)
	commRepo := cspg.NewCommunicationRepository(pool)

	// Cross-context bridges (inline SQL — see below).
	resolver := &mentionResolverBridge{pool: pool}
	notifier := newNotificationBridge(pool, log)
	ctResolver := &customerTypeResolverBridge{pool: pool, log: log}
	woBridge := &woFromTicketBridge{pool: pool, log: log}
	csatDispatcher := newCSATInviteDispatcherBridge(pool, log)

	// Usecases
	ticketSvc := csusecase.NewTicketService(ticketRepo, eventRepo, notifier)
	commentSvc := csusecase.NewCommentService(ticketRepo, commentRepo, mentionRepo, eventRepo, resolver, notifier)
	mentionSvc := csusecase.NewMentionService(mentionRepo, notifier)
	channelSvc := csusecase.NewChannelService(channelRepo)

	// Wave 124 usecases
	slaSvc := csusecase.NewSLAService(slaMatrixRepo, ticketRepo, eventRepo, ctResolver, notifier, ticketRepo)
	srSvc := csusecase.NewServiceRequestService(srRepo, ticketSvc, eventRepo).WithWOBridge(woBridge)
	teamSvc := csusecase.NewTeamService(teamRepo, teamMemberRepo, ticketRepo, assignHistRepo, eventRepo, notifier)
	wftSvc := csusecase.NewWOFromTicketService(ticketRepo, eventRepo, woBridge)
	csatSvc := csusecase.NewCSATService(csatRepo, ticketRepo, eventRepo, csatDispatcher, notifier)
	commSvc := csusecase.NewCommunicationService(commRepo, ticketRepo, eventRepo)

	// Inject Wave 124 add-ons into TicketService.
	ticketSvc.WithSLA(slaSvc).WithCSAT(csatSvc).WithAssignmentHistory(assignHistRepo)

	verifier := auth.NewVerifier(cfg.JWTSecret, cfg.JWTIssuer)

	// Wave 126 — CS Dashboard backend aggregations.
	dashRepo := opspg.NewCSDashboardAggregationRepository(pool)
	dashLive := opspg.NewCSDashboardLiveReader(pool)
	dashboardSvc := csusecase.NewCSDashboardService(csusecase.CSDashboardDeps{
		Repo: dashRepo,
		Live: dashLive,
		Log:  log,
	})

	handler := cshttp.NewHandler(ticketSvc, commentSvc, mentionSvc, channelSvc, verifier).
		WithSLA(slaSvc).
		WithServiceRequests(srSvc).
		WithTeams(teamSvc).
		WithWOFromTicket(wftSvc).
		WithCSAT(csatSvc).
		WithCommunications(commSvc).
		WithCSDashboards(dashboardSvc)

	serverCfg := httpserver.DefaultConfig(cfg.HTTPPort)
	serverCfg.PrometheusServiceName = "cs-svc"
	server := httpserver.New(serverCfg, log)
	server.SetHealth("cs-svc", pool.Ping)
	handler.Mount(server.Router)
	handler.MountWave126(server.Router)

	// Cron
	runner := cscron.New(log).
		WithTicketService(ticketSvc).
		WithMentionService(mentionSvc).
		WithAutoCloseRepo(ticketRepo).
		WithSLAService(slaSvc).
		WithCSATService(csatSvc).
		WithDashboardService(dashboardSvc)
	runner.Start(ctx)

	if err := server.Run(ctx); err != nil {
		log.Error("http server stopped with error", "err", err)
		os.Exit(1)
	}
	log.Info("cs-svc stopped")
}

// =====================================================================
// Cross-context bridges — inline SQL-only adapters.
//
// Same pattern as cmd/hris-svc/main.go: kept here so internal/cs/
// stays free of cross-context imports while still letting the comment
// flow ping notifyx + resolve @usernames to identity.users.id.
// =====================================================================

// mentionResolverBridge implements port.MentionResolver. The
// identity.users table doesn't yet carry a dedicated `username`
// column; the resolver falls back to the email local-part (the part
// before @) which is the conventional handle in IT shops. Unknown
// names are silently dropped — typos in @maybeATypo MUST NOT fail
// the comment save.
type mentionResolverBridge struct {
	pool *pgxpool.Pool
}

var _ csport.MentionResolver = (*mentionResolverBridge)(nil)

func (b *mentionResolverBridge) Resolve(ctx context.Context, usernames []string) (map[string]uuid.UUID, error) {
	if b == nil || b.pool == nil || len(usernames) == 0 {
		return nil, nil
	}
	out := make(map[string]uuid.UUID, len(usernames))
	rows, err := b.pool.Query(ctx, `
		SELECT id, LOWER(SPLIT_PART(email, '@', 1)) AS handle
		  FROM identity.users
		 WHERE active = TRUE
		   AND LOWER(SPLIT_PART(email, '@', 1)) = ANY($1)
	`, usernames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var handle string
		if err := rows.Scan(&id, &handle); err != nil {
			return nil, err
		}
		out[strings.ToLower(handle)] = id
	}
	return out, nil
}

// notificationBridge wraps notifyx.Dispatcher. NotifyMention sends a
// targeted push to the mentioned user; NotifyAssignment pings the
// assignee. Both are best-effort — a failed push must not fail the
// upstream DB write.
type notificationBridge struct {
	disp *notifyx.Dispatcher
	log  *slog.Logger
}

func newNotificationBridge(pool *pgxpool.Pool, log *slog.Logger) *notificationBridge {
	return &notificationBridge{
		disp: notifyx.New(pool, log),
		log:  log.With("component", "cs.notifier"),
	}
}

var _ csport.NotificationBridge = (*notificationBridge)(nil)

func (b *notificationBridge) NotifyMention(ctx context.Context, m *domain.Mention, ticketTitle, mentionedByName string) {
	if b == nil || b.disp == nil || m == nil {
		return
	}
	body := "You were mentioned on a ticket"
	if ticketTitle != "" {
		body = "You were mentioned: " + ticketTitle
	}
	b.disp.Send(ctx, notifyx.Target{UserID: m.MentionedUserID}, notifyx.Message{
		Title:    "CS mention",
		Body:     body,
		DeepLink: "app://cs/tickets/" + m.TicketID.String(),
		Topic:    "cs_mention",
		Data: map[string]string{
			"mention_id": m.ID.String(),
			"ticket_id":  m.TicketID.String(),
		},
	})
}

func (b *notificationBridge) NotifyAssignment(ctx context.Context, ticketID, assignedUserID uuid.UUID, ticketTitle string) {
	if b == nil || b.disp == nil {
		return
	}
	body := "You were assigned a ticket"
	if ticketTitle != "" {
		body = "Assigned: " + ticketTitle
	}
	b.disp.Send(ctx, notifyx.Target{UserID: assignedUserID}, notifyx.Message{
		Title:    "CS ticket assigned",
		Body:     body,
		DeepLink: "app://cs/tickets/" + ticketID.String(),
		Topic:    "cs_assignment",
		Data: map[string]string{
			"ticket_id": ticketID.String(),
		},
	})
}

// =====================================================================
// Wave 124 cross-context bridges
// =====================================================================

// customerTypeResolverBridge queries crm.customers.customer_type and
// maps the CRM value (broadband/business/enterprise/corporate) into
// the CS-side domain.CustomerType enum via MapCRMCustomerType. Missing
// rows fall back to residential — SLA assignment never fails because
// the customer row is gone or has a NULL type.
type customerTypeResolverBridge struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ csport.CustomerTypeResolver = (*customerTypeResolverBridge)(nil)

func (b *customerTypeResolverBridge) Resolve(ctx context.Context, customerID uuid.UUID) (domain.CustomerType, error) {
	if b == nil || b.pool == nil {
		return domain.CustomerTypeResidential, nil
	}
	var raw string
	err := b.pool.QueryRow(ctx,
		`SELECT COALESCE(customer_type, '') FROM crm.customers WHERE id = $1`,
		customerID,
	).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CustomerTypeResidential, nil
	}
	if err != nil {
		// best-effort: log + default. SLA assignment must not fail.
		if b.log != nil {
			b.log.Warn("cs.customer_type_resolver query failed", "err", err, "customer_id", customerID)
		}
		return domain.CustomerTypeResidential, nil
	}
	return domain.MapCRMCustomerType(raw), nil
}

// woFromTicketBridge writes a field.work_orders row with wo_type =
// 'maintenance' (the closest existing enum slot for a ticket-driven
// WO; the wo_type enum is locked to {new_installation, maintenance,
// termination}). The link back to the source ticket is carried via
// the WO's notes field as `[from-ticket: <ticket-uuid>]` — sufficient
// for the Wave 124 test surface; Wave 127 will add a dedicated
// source_ticket_id column.
type woFromTicketBridge struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ csport.WOFromTicketBridge = (*woFromTicketBridge)(nil)

func (b *woFromTicketBridge) CreateWOFromTicket(ctx context.Context, ticketID uuid.UUID, woTemplateID *uuid.UUID, scheduledAt *time.Time, createdBy uuid.UUID) (uuid.UUID, error) {
	if b == nil || b.pool == nil {
		return uuid.Nil, derrors.Internal("cs.wft.no_pool", "wo-from-ticket bridge has no db pool")
	}
	// Pull the source ticket so we can hand the customer_id +
	// address fields to field.work_orders.
	var customerID uuid.UUID
	var title, description string
	err := b.pool.QueryRow(ctx,
		`SELECT customer_id, title, COALESCE(description,'') FROM cs.tickets WHERE id = $1`,
		ticketID,
	).Scan(&customerID, &title, &description)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, derrors.NotFound("cs.wft.ticket_not_found", "source ticket not found")
	}
	if err != nil {
		return uuid.Nil, derrors.Wrap(derrors.KindInternal, "cs.wft.load_ticket", "load source ticket", err)
	}

	// Pull the customer's address from crm.customers as the WO address
	// fallback. Empty string is OK — the field schema allows blank
	// addresses on maintenance WOs.
	var address string
	_ = b.pool.QueryRow(ctx,
		`SELECT COALESCE(address,'') FROM crm.customers WHERE id = $1`,
		customerID,
	).Scan(&address)
	if strings.TrimSpace(address) == "" {
		address = title // last-resort placeholder
	}
	if strings.TrimSpace(address) == "" {
		address = "from-ticket:" + ticketID.String()
	}

	woID := uuid.New()
	woNo := "WO-" + time.Now().UTC().Format("20060102") + "-" + woID.String()[:8]
	notes := "[from-ticket:" + ticketID.String() + "] " + description
	priority := "medium"
	status := "created"

	var schedPtr any
	if scheduledAt != nil {
		schedPtr = scheduledAt.UTC()
	}

	_, err = b.pool.Exec(ctx, `
		INSERT INTO field.work_orders
			(id, wo_number, customer_id, wo_type, product_type,
			 address, priority, status, scheduled_date, sla_due_at,
			 is_emergency, is_cross_area, notes, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, 'maintenance', 'broadband',
		        $4, $5, $6, $7, NULL,
		        FALSE, FALSE, $8, $9, NOW(), NOW())
	`,
		woID, woNo, customerID, address, priority, status, schedPtr, notes, createdBy,
	)
	if err != nil {
		return uuid.Nil, derrors.Wrap(derrors.KindInternal, "cs.wft.insert_wo", "insert work order", err)
	}
	if b.log != nil {
		b.log.Info("cs wo created from ticket", "wo_id", woID, "ticket_id", ticketID, "customer_id", customerID)
	}
	return woID, nil
}

// csatInviteDispatcherBridge wraps notifyx.Dispatcher with channel-aware
// message templating. Wave 124 routes every channel through the
// in-app push (notifyx) so the customer sees the invite in the portal
// inbox. Email / WhatsApp / SMS dispatch will plug into the same
// interface in Wave 127.
type csatInviteDispatcherBridge struct {
	disp *notifyx.Dispatcher
	pool *pgxpool.Pool
	log  *slog.Logger
}

func newCSATInviteDispatcherBridge(pool *pgxpool.Pool, log *slog.Logger) *csatInviteDispatcherBridge {
	return &csatInviteDispatcherBridge{
		disp: notifyx.New(pool, log),
		pool: pool,
		log:  log.With("component", "cs.csat_dispatcher"),
	}
}

var _ csport.CSATInviteDispatcher = (*csatInviteDispatcherBridge)(nil)

func (b *csatInviteDispatcherBridge) SendInvite(ctx context.Context, ticketID, customerID uuid.UUID, channel string) error {
	if b == nil || b.disp == nil {
		return derrors.Internal("cs.csat.no_dispatcher", "csat dispatcher has no notifyx instance")
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" {
		channel = "email"
	}
	title := "How was your experience?"
	body := "Please rate the support you received for your recent ticket."
	deepLink := "app://cs/tickets/" + ticketID.String() + "/csat"
	b.disp.Send(ctx, notifyx.Target{CustomerID: customerID}, notifyx.Message{
		Title:    title,
		Body:     body,
		DeepLink: deepLink,
		Topic:    "cs_csat_invite",
		Data: map[string]string{
			"ticket_id":   ticketID.String(),
			"customer_id": customerID.String(),
			"channel":     channel,
		},
	})
	return nil
}
