package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// WhoisParserService owns the per-TLD WHOIS rules configured in Admin > TLD/SSL
// expiration.
//
// The rules are the escape hatch for the TLDs without RDAP and for the ones that
// do not publish the expiry in a machine readable way: the operator teaches Up
// how to read that registry once, and every monitor on the TLD benefits.
type WhoisParserService struct {
	db  *gorm.DB
	cfg *config.Config
	log *slog.Logger
}

// NewWhoisParserService builds the parser service.
func NewWhoisParserService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *WhoisParserService {
	return &WhoisParserService{db: db, cfg: cfg, log: log}
}

// All returns every parser, ordered by suffix (what the admin page shows).
func (s *WhoisParserService) All(ctx context.Context) ([]models.WhoisParser, error) {
	var parsers []models.WhoisParser
	if err := s.db.WithContext(ctx).Order("tld ASC").Find(&parsers).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the whois parsers: %w", err))
	}
	return parsers, nil
}

// Enabled returns the enabled parsers (what a lookup run uses).
func (s *WhoisParserService) Enabled(ctx context.Context) ([]models.WhoisParser, error) {
	var parsers []models.WhoisParser
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Order("tld ASC").Find(&parsers).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the whois parsers: %w", err))
	}
	return parsers, nil
}

// Get loads one parser.
func (s *WhoisParserService) Get(ctx context.Context, id uint) (*models.WhoisParser, error) {
	var parser models.WhoisParser
	err := s.db.WithContext(ctx).First(&parser, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound(i18n.CodeNotFound, fmt.Sprintf("whois parser %d does not exist", id))
	}
	if err != nil {
		return nil, ErrInternal(err)
	}
	return &parser, nil
}

// Create validates and stores a rule.
func (s *WhoisParserService) Create(ctx context.Context, parser *models.WhoisParser) (*models.WhoisParser, error) {
	parser.Normalize()
	if problem := parser.Validate(); problem != "" {
		return nil, ErrBadRequest(i18n.CodeValidation, problem)
	}
	if parser.Server == "" && parser.TLD == "" {
		return nil, ErrBadRequest(i18n.CodeValidation, "tld is required")
	}
	var existing int64
	if err := s.db.WithContext(ctx).Model(&models.WhoisParser{}).
		Where("tld = ?", parser.TLD).Count(&existing).Error; err != nil {
		return nil, ErrInternal(err)
	}
	if existing > 0 {
		return nil, ErrConflict(i18n.CodeAlreadyExists, "a whois parser for "+parser.TLD+" already exists")
	}
	if err := s.db.WithContext(ctx).Create(parser).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("creating the whois parser: %w", err))
	}
	s.log.Info("whois parser created", "id", parser.ID, "tld", parser.TLD)
	return parser, nil
}

// Update rewrites a rule.
func (s *WhoisParserService) Update(ctx context.Context, id uint, parser *models.WhoisParser) (*models.WhoisParser, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	parser.ID = current.ID
	parser.CreatedAt = current.CreatedAt
	parser.Normalize()
	if problem := parser.Validate(); problem != "" {
		return nil, ErrBadRequest(i18n.CodeValidation, problem)
	}
	var clash int64
	if err := s.db.WithContext(ctx).Model(&models.WhoisParser{}).
		Where("tld = ? AND id <> ?", parser.TLD, id).Count(&clash).Error; err != nil {
		return nil, ErrInternal(err)
	}
	if clash > 0 {
		return nil, ErrConflict(i18n.CodeAlreadyExists, "a whois parser for "+parser.TLD+" already exists")
	}
	if err := s.db.WithContext(ctx).Model(&models.WhoisParser{}).Where("id = ?", id).
		Select("tld", "server", "expiry_regex", "date_layouts", "not_found_pattern", "min_interval_ms", "enabled", "note", "updated_at").
		Updates(parser).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("updating the whois parser: %w", err))
	}
	s.log.Info("whois parser updated", "id", id, "tld", parser.TLD)
	return s.Get(ctx, id)
}

// Delete removes a rule.
func (s *WhoisParserService) Delete(ctx context.Context, id uint) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Delete(&models.WhoisParser{}, id).Error; err != nil {
		return ErrInternal(fmt.Errorf("deleting the whois parser: %w", err))
	}
	s.log.Info("whois parser deleted", "id", id)
	return nil
}

// ValidateWhoisParser validates a rule without touching the database (used by
// the "test" endpoint, where the operator pastes a draft).
func ValidateWhoisParser(parser *models.WhoisParser) error {
	candidate := *parser
	candidate.Normalize()
	if problem := candidate.Validate(); problem != "" {
		return ErrBadRequest(i18n.CodeValidation, problem)
	}
	return nil
}
