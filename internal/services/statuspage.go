package services

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// slugPattern validates the status page slug (used in the public URL).
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// StatusPageService owns the public status pages: CRUD, monitor selection and
// the aggregated public payload.
type StatusPageService struct {
	db       *gorm.DB
	cfg      *config.Config
	log      *slog.Logger
	monitors *MonitorService
	// emit publishes the local writes to the peers (nil outside federated mode).
	emit *SyncEmitter
}

// NewStatusPageService builds the status page service.
func NewStatusPageService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *StatusPageService {
	return &StatusPageService{db: db, cfg: cfg, log: log}
}

// SetMonitorService injects the monitor service so public payloads can be
// decorated with status, uptime and heartbeat bars.
func (s *StatusPageService) SetMonitorService(m *MonitorService) { s.monitors = m }

// SetSyncEmitter injects the publisher of the synchronisation outbox.
func (s *StatusPageService) SetSyncEmitter(emitter *SyncEmitter) { s.emit = emitter }

// publishStatusPage records the page in the outbox, inside the caller's
// transaction. The payload carries the whole selection, order and overrides
// included (see BuildStatusPagePayload).
func (s *StatusPageService) publishStatusPage(ctx context.Context, tx *gorm.DB, id uint) error {
	if s.emit == nil {
		return nil
	}
	return s.emit.EmitStatusPage(ctx, tx, id)
}

// statusPageUUID reads the global identity of a page (needed before a delete).
func (s *StatusPageService) statusPageUUID(ctx context.Context, tx *gorm.DB, id uint) (string, error) {
	if s.emit == nil {
		return "", nil
	}
	return s.emit.RowUUID(ctx, tx, models.EntityStatusPage, id)
}

// tombstoneStatusPage publishes the removal of a page.
func (s *StatusPageService) tombstoneStatusPage(ctx context.Context, tx *gorm.DB, uuid string) error {
	if s.emit == nil || uuid == "" {
		return nil
	}
	return s.emit.EmitDelete(ctx, tx, models.EntityStatusPage, uuid)
}

// touchStatusPage advances the revision of a page whose SELECTION changed, and
// publishes it.
//
// The selection lives inside the page payload, so an edit to it is a new version of
// the page and has to advance the revision: without it a peer would compare the
// same revision, skip the change as not newer, and the two nodes would show
// different pages for ever. A page with no monitor and no group is still published,
// because "the selection is now empty" is exactly the change a peer must apply.
func (s *StatusPageService) touchStatusPage(ctx context.Context, tx *gorm.DB, id uint) error {
	if s.emit == nil {
		return nil
	}
	if err := tx.WithContext(ctx).Model(&models.StatusPage{}).Where("id = ?", id).
		Updates(map[string]any{
			"revision":   gorm.Expr("revision + 1"),
			"updated_at": time.Now().UTC(),
		}).Error; err != nil {
		return fmt.Errorf("advancing the revision of status page %d: %w", id, err)
	}
	return s.emit.EmitStatusPage(ctx, tx, id)
}

// List returns every status page with a monitor count. The count includes the
// monitors that arrive through the linked groups, so the admin list shows what
// the page really renders.
func (s *StatusPageService) List(ctx context.Context) ([]*models.StatusPage, error) {
	var pages []*models.StatusPage
	if err := s.db.WithContext(ctx).Order("title ASC").Find(&pages).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing status pages: %w", err))
	}
	var items []models.StatusPageMonitor
	if err := s.db.WithContext(ctx).Find(&items).Error; err != nil {
		return nil, ErrInternal(err)
	}
	var links []models.StatusPageGroupLink
	if err := s.db.WithContext(ctx).Find(&links).Error; err != nil {
		return nil, ErrInternal(err)
	}

	explicit := map[uint]map[uint]bool{}
	for _, item := range items {
		if explicit[item.StatusPageID] == nil {
			explicit[item.StatusPageID] = map[uint]bool{}
		}
		explicit[item.StatusPageID][item.MonitorID] = true
	}
	groupIDs := make([]uint, 0, len(links))
	for _, link := range links {
		groupIDs = append(groupIDs, link.GroupID)
	}
	members, err := s.groupMembers(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	counts := map[uint]int{}
	for pageID, ids := range explicit {
		counts[pageID] = len(ids)
	}
	groupCounts := map[uint]int{}
	for _, link := range links {
		groupCounts[link.StatusPageID]++
		for _, monitorID := range members[link.GroupID] {
			if explicit[link.StatusPageID][monitorID] {
				continue
			}
			counts[link.StatusPageID]++
		}
	}
	for _, page := range pages {
		page.ItemCount = counts[page.ID]
		page.GroupCount = groupCounts[page.ID]
	}
	return pages, nil
}

// Get loads a status page by id.
func (s *StatusPageService) Get(ctx context.Context, id uint) (*models.StatusPage, error) {
	var page models.StatusPage
	err := s.db.WithContext(ctx).First(&page, id).Error
	if err == gorm.ErrRecordNotFound {
		return nil, ErrNotFound(i18n.CodeStatusPageNotFound, fmt.Sprintf("status page %d does not exist", id))
	}
	if err != nil {
		return nil, ErrInternal(err)
	}
	return &page, nil
}

// GetBySlug loads a status page by slug (public endpoint).
func (s *StatusPageService) GetBySlug(ctx context.Context, slug string) (*models.StatusPage, error) {
	var page models.StatusPage
	err := s.db.WithContext(ctx).Where("slug = ?", strings.ToLower(strings.TrimSpace(slug))).First(&page).Error
	if err == gorm.ErrRecordNotFound {
		return nil, ErrNotFound(i18n.CodeStatusPageNotFound, "status page "+slug+" does not exist")
	}
	if err != nil {
		return nil, ErrInternal(err)
	}
	return &page, nil
}

// Create stores a new status page, selection included.
//
// The selection is written in the SAME transaction and published as one version: the
// page used to be created and then edited separately, which put two revisions and two
// outbox rows on the wire for one operator action — and made "the page as created" a
// state no peer ever saw.
func (s *StatusPageService) Create(ctx context.Context, page *models.StatusPage, monitorIDs []uint) error {
	if err := s.normalize(page); err != nil {
		return err
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.StatusPage{}).Where("slug = ?", page.Slug).Count(&count).Error; err != nil {
		return ErrInternal(err)
	}
	if count > 0 {
		return ErrConflict(i18n.CodeSlugTaken, "the slug "+page.Slug+" is already in use")
	}
	// The identity is stamped here rather than by a later update, so the row, the
	// payload and the response all name the same creator from the start.
	page.OriginNodeID = s.cfg.NodeID
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(page).Error; err != nil {
			return err
		}
		if len(monitorIDs) > 0 {
			if err := writeMonitorSelection(tx, page.ID, monitorIDs); err != nil {
				return err
			}
		}
		return s.publishStatusPage(ctx, tx, page.ID)
	}); err != nil {
		return ErrInternal(err)
	}
	s.log.Info("status page created", "id", page.ID, "slug", page.Slug, "monitors", len(monitorIDs))
	return nil
}

// Update saves an existing status page.
func (s *StatusPageService) Update(ctx context.Context, page *models.StatusPage) error {
	if err := s.normalize(page); err != nil {
		return err
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.StatusPage{}).
		Where("slug = ? AND id <> ?", page.Slug, page.ID).Count(&count).Error; err != nil {
		return ErrInternal(err)
	}
	if count > 0 {
		return ErrConflict(i18n.CodeSlugTaken, "the slug "+page.Slug+" is already in use")
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.StatusPage{}).Where("id = ?", page.ID).Updates(map[string]any{
			"slug":        page.Slug,
			"title":       page.Title,
			"description": page.Description,
			"footer_text": page.FooterText,
			"theme":       page.Theme,
			"is_public":   page.IsPublic,
			"show_uptime": page.ShowUptime,
			"show_charts": page.ShowCharts,
			"show_tags":   page.ShowTags,
			"show_expiry": page.ShowExpiry,
			"custom_css":  page.CustomCSS,
			// Every edit advances the revision: it is the primary component of the
			// merge order (see the group update).
			"revision":   gorm.Expr("revision + 1"),
			"updated_at": time.Now().UTC(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return s.publishStatusPage(ctx, tx, page.ID)
	})
	if err == gorm.ErrRecordNotFound {
		return ErrNotFound(i18n.CodeStatusPageNotFound, fmt.Sprintf("status page %d does not exist", page.ID))
	}
	if err != nil {
		return ErrInternal(err)
	}
	s.log.Info("status page updated", "id", page.ID, "slug", page.Slug)
	return nil
}

// Delete removes a status page and its monitor selection.
func (s *StatusPageService) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The identity is read BEFORE the cascade: once the row is gone its uuid
		// cannot be read any more, and the tombstone has to name it.
		uuid, err := s.statusPageUUID(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := tx.Where("status_page_id = ?", id).Delete(&models.StatusPageMonitor{}).Error; err != nil {
			return err
		}
		if err := tx.Where("status_page_id = ?", id).Delete(&models.StatusPageGroupLink{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.StatusPage{}, id).Error; err != nil {
			return err
		}
		return s.tombstoneStatusPage(ctx, tx, uuid)
	})
	if err != nil {
		return ErrInternal(err)
	}
	s.log.Info("status page deleted", "id", id)
	return nil
}

// normalize validates and cleans the input.
func (s *StatusPageService) normalize(page *models.StatusPage) error {
	page.Slug = strings.ToLower(strings.TrimSpace(page.Slug))
	page.Title = strings.TrimSpace(page.Title)
	if page.Title == "" {
		return ErrBadRequest(i18n.CodeValidation, "title is required")
	}
	if !slugPattern.MatchString(page.Slug) {
		return ErrBadRequest(i18n.CodeValidation,
			"slug must contain lowercase letters, numbers and dashes only (e.g. my-status)")
	}
	if !containsString(config.SupportedThemes, page.Theme) {
		page.Theme = "system"
	}
	return nil
}

// Items returns the monitor selection of a status page (ordered).
func (s *StatusPageService) Items(ctx context.Context, pageID uint) ([]models.StatusPageMonitor, error) {
	var items []models.StatusPageMonitor
	err := s.db.WithContext(ctx).
		Where("status_page_id = ?", pageID).
		Order("sort_order ASC, id ASC").
		Find(&items).Error
	if err != nil {
		return nil, ErrInternal(err)
	}
	return items, nil
}
