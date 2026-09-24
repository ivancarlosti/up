package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// CertificateService keeps the TLS certificate of every monitor that watches one
// and decides when a reminder must be sent.
//
// The capture happens on the probe (the certificate is a property of the
// handshake), so this service never opens a socket: it stores what the scheduler
// read and, on a schedule, turns "days left" into the notifications.
type CertificateService struct {
	db            *gorm.DB
	cfg           *config.Config
	log           *slog.Logger
	hub           EventPublisher
	monitors      *MonitorService
	notifications *NotificationService
	cluster       *ClusterService
}

// NewCertificateService builds the certificate service.
func NewCertificateService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *CertificateService {
	return &CertificateService{db: db, cfg: cfg, log: log}
}

// SetPublisher injects the real time publisher.
func (s *CertificateService) SetPublisher(p EventPublisher) { s.hub = p }

// SetMonitorService injects the monitor service (the watcher needs the monitor
// configuration to know what to watch).
func (s *CertificateService) SetMonitorService(m *MonitorService) { s.monitors = m }

// SetNotificationService injects the channel dispatcher.
func (s *CertificateService) SetNotificationService(n *NotificationService) { s.notifications = n }

// SetClusterService injects the service that elects the notification sender.
func (s *CertificateService) SetClusterService(c *ClusterService) { s.cluster = c }

func (s *CertificateService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}

// Record stores the certificate read by a probe and reports whether it changed.
//
// The notification bookkeeping (notified thresholds, last notified day) is
// preserved: only the certificate columns are written, so a monitor that is
// checked every minute does not lose the memory of what it already alerted.
func (s *CertificateService) Record(ctx context.Context, monitorID uint, nodeID string, info *models.CertificateInfo) (bool, error) {
	if info == nil {
		return false, nil
	}
	if info.CapturedAt.IsZero() {
		info.CapturedAt = time.Now().UTC()
	}
	current, err := s.Get(ctx, monitorID)
	if err != nil {
		return false, err
	}
	// The observation is clipped to what the columns can hold before anything
	// else: a certificate that lists hundreds of subject alternative names is
	// stored (the tail of the list is dropped and logged) instead of failing the
	// insert with "Data too long for column 'dns_names'", which used to leave
	// the monitor without a certificate and without its reminders.
	dnsNames, droppedNames := models.EncodeDNSNames(info.DNSNames)
	row := models.MonitorCertificate{
		MonitorID:      monitorID,
		Subject:        trimmed(info.Subject, models.MaxCertSubjectLen),
		Issuer:         trimmed(info.Issuer, models.MaxCertIssuerLen),
		Serial:         trimmed(info.Serial, models.MaxCertSerialLen),
		NotBefore:      info.NotBefore,
		NotAfter:       info.NotAfter,
		DNSNames:       dnsNames,
		DaysLeft:       info.DaysLeft,
		CapturedAt:     info.CapturedAt,
		CapturedByNode: nodeID,
	}
	if droppedNames > 0 {
		s.log.Warn("the certificate lists more subject alternative names than Up stores",
			"monitor_id", monitorID, "dropped", droppedNames, "budget_bytes", models.MaxCertDNSNames)
	}

	// The row is written at most once a day per target, and immediately when the
	// certificate itself changes. A monitor checked every minute used to rewrite
	// the same row 1440 times a day; the expiry data only ages once a day, so
	// nothing is lost by skipping the identical writes. The comparison uses the
	// clipped row, so a clipped value cannot look "changed" on every probe.
	changed := current == nil ||
		!current.NotAfter.Equal(row.NotAfter) ||
		!strings.EqualFold(current.Serial, row.Serial) ||
		current.DaysLeft != row.DaysLeft ||
		dayBucket(current.CapturedAt) != dayBucket(row.CapturedAt)
	if !changed {
		return false, nil
	}
	// The columns of the notification memory are only set on insert: an update
	// must not reset them.
	if current == nil {
		row.NotifiedDays = ""
		row.LastNotifiedDay = 0
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "monitor_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"subject", "issuer", "serial", "not_before", "not_after", "dns_names",
			"days_left", "captured_at", "captured_by_node",
		}),
	}).Create(&row)
	if result.Error != nil {
		return false, ErrInternal(fmt.Errorf("storing the certificate of monitor %d: %w", monitorID, result.Error))
	}
	if changed {
		s.log.Info("certificate captured",
			"monitor_id", monitorID, "issuer", info.Issuer,
			"not_after", info.NotAfter.Format(time.RFC3339), "days_left", info.DaysLeft, "node_id", nodeID)
	}
	return changed, nil
}

// Get returns the stored certificate of a monitor (nil when there is none).
func (s *CertificateService) Get(ctx context.Context, monitorID uint) (*models.MonitorCertificate, error) {
	var row models.MonitorCertificate
	err := s.db.WithContext(ctx).Where("monitor_id = ?", monitorID).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, ErrInternal(err)
	}
	return &row, nil
}

// All returns the certificates of the monitors that watch one, with their
// monitor loaded: this is what the watcher job iterates.
func (s *CertificateService) All(ctx context.Context) ([]models.MonitorCertificate, []*models.Monitor, error) {
	var rows []models.MonitorCertificate
	if err := s.db.WithContext(ctx).
		Joins("JOIN monitors ON monitors.id = monitor_certificates.monitor_id").
		Where("monitors.cert_watch = ?", true).
		Order("monitor_certificates.monitor_id ASC").
		Find(&rows).Error; err != nil {
		return nil, nil, ErrInternal(err)
	}
	if len(rows) == 0 {
		return rows, nil, nil
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.MonitorID)
	}
	var monitors []*models.Monitor
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&monitors).Error; err != nil {
		return nil, nil, ErrInternal(err)
	}
	byID := map[uint]*models.Monitor{}
	for _, monitor := range monitors {
		byID[monitor.ID] = monitor
	}
	ordered := make([]*models.Monitor, 0, len(rows))
	for _, row := range rows {
		if monitor, ok := byID[row.MonitorID]; ok {
			ordered = append(ordered, monitor)
		}
	}
	return rows, ordered, nil
}
