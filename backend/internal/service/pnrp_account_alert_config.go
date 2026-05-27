package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const (
	SettingKeyPNRPAccountAlertConfig = "pnrp_account_alert_config"

	pnrpAccountAlertDefaultCooldownMinutes          = 60
	pnrpAccountAlertDefaultMinAvailableAccounts     = 1
	pnrpAccountAlertDefaultAvailabilityCheckMinutes = 5
	pnrpAccountAlertMinCooldownMinutes              = 5
	pnrpAccountAlertMaxCooldownMinutes              = 1440
	pnrpAccountAlertMinAvailabilityCheckMinutes     = 1
	pnrpAccountAlertMaxAvailabilityCheckMinutes     = 1440
	pnrpAccountAlertMaxRecipientCount               = 20
)

type PNRPAccountAlertConfig struct {
	Enabled                          bool     `json:"enabled"`
	UseOpsEmailRecipients            bool     `json:"use_ops_email_recipients"`
	Recipients                       []string `json:"recipients"`
	ScheduledTestFailureEnabled      bool     `json:"scheduled_test_failure_enabled"`
	RateLimitFailureEnabled          bool     `json:"rate_limit_failure_enabled"`
	ErrorFailureEnabled              bool     `json:"error_failure_enabled"`
	MinAvailableAccountsEnabled      bool     `json:"min_available_accounts_enabled"`
	MinAvailableAccounts             int      `json:"min_available_accounts"`
	CooldownMinutes                  int      `json:"cooldown_minutes"`
	AvailabilityCheckIntervalMinutes int      `json:"availability_check_interval_minutes"`
}

type PNRPAccountAlertConfigService struct {
	settingRepo SettingRepository
}

func NewPNRPAccountAlertConfigService(settingRepo SettingRepository) *PNRPAccountAlertConfigService {
	return &PNRPAccountAlertConfigService{settingRepo: settingRepo}
}

func DefaultPNRPAccountAlertConfig() PNRPAccountAlertConfig {
	return PNRPAccountAlertConfig{
		Enabled:                          true,
		UseOpsEmailRecipients:            true,
		Recipients:                       []string{},
		ScheduledTestFailureEnabled:      true,
		RateLimitFailureEnabled:          true,
		ErrorFailureEnabled:              true,
		MinAvailableAccountsEnabled:      false,
		MinAvailableAccounts:             pnrpAccountAlertDefaultMinAvailableAccounts,
		CooldownMinutes:                  pnrpAccountAlertDefaultCooldownMinutes,
		AvailabilityCheckIntervalMinutes: pnrpAccountAlertDefaultAvailabilityCheckMinutes,
	}
}

func (s *PNRPAccountAlertConfigService) GetConfig(ctx context.Context) (PNRPAccountAlertConfig, error) {
	cfg := DefaultPNRPAccountAlertConfig()
	if s == nil || s.settingRepo == nil {
		return cfg, nil
	}

	raw, err := s.settingRepo.GetValue(ctx, SettingKeyPNRPAccountAlertConfig)
	if err != nil || strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return DefaultPNRPAccountAlertConfig(), nil
	}
	return NormalizePNRPAccountAlertConfig(cfg), nil
}

func (s *PNRPAccountAlertConfigService) UpdateConfig(ctx context.Context, cfg PNRPAccountAlertConfig) (PNRPAccountAlertConfig, error) {
	cfg = NormalizePNRPAccountAlertConfig(cfg)
	if s == nil || s.settingRepo == nil {
		return cfg, nil
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyPNRPAccountAlertConfig, string(data)); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func NormalizePNRPAccountAlertConfig(cfg PNRPAccountAlertConfig) PNRPAccountAlertConfig {
	cfg.Recipients = pnrpDeduplicateEmails(cfg.Recipients)
	if len(cfg.Recipients) > pnrpAccountAlertMaxRecipientCount {
		cfg.Recipients = cfg.Recipients[:pnrpAccountAlertMaxRecipientCount]
	}
	if cfg.CooldownMinutes <= 0 {
		cfg.CooldownMinutes = pnrpAccountAlertDefaultCooldownMinutes
	}
	cfg.CooldownMinutes = clampInt(cfg.CooldownMinutes, pnrpAccountAlertMinCooldownMinutes, pnrpAccountAlertMaxCooldownMinutes)
	if cfg.MinAvailableAccounts <= 0 {
		cfg.MinAvailableAccounts = pnrpAccountAlertDefaultMinAvailableAccounts
	}
	if cfg.AvailabilityCheckIntervalMinutes <= 0 {
		cfg.AvailabilityCheckIntervalMinutes = pnrpAccountAlertDefaultAvailabilityCheckMinutes
	}
	cfg.AvailabilityCheckIntervalMinutes = clampInt(
		cfg.AvailabilityCheckIntervalMinutes,
		pnrpAccountAlertMinAvailabilityCheckMinutes,
		pnrpAccountAlertMaxAvailabilityCheckMinutes,
	)
	return cfg
}

func (c PNRPAccountAlertConfig) Cooldown() time.Duration {
	c = NormalizePNRPAccountAlertConfig(c)
	return time.Duration(c.CooldownMinutes) * time.Minute
}

func (c PNRPAccountAlertConfig) AvailabilityCheckInterval() time.Duration {
	c = NormalizePNRPAccountAlertConfig(c)
	return time.Duration(c.AvailabilityCheckIntervalMinutes) * time.Minute
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
