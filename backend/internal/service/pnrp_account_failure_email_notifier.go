package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

const (
	pnrpAccountAlertSettingPrefix = "pnrp_account_alert:"
)

const (
	pnrpAccountAlertKindScheduledTest = "scheduled_test"
	pnrpAccountAlertKindAvailability  = "availability"
)

// ScheduledTestFailureNotifier is a narrow hook used by the scheduled test runner.
// PNRP custom alerting lives behind this interface so the upstream runner only
// needs a small optional call site.
type ScheduledTestFailureNotifier interface {
	NotifyScheduledTestFailure(ctx context.Context, plan *ScheduledTestPlan, result *ScheduledTestResult)
}

type PNRPAccountFailureEmailNotifier struct {
	accountRepo  AccountRepository
	opsService   *OpsService
	emailService *EmailService
	settingRepo  SettingRepository
	configSvc    *PNRPAccountAlertConfigService
}

type pnrpAccountFailureAlertState struct {
	ErrorHash string    `json:"error_hash"`
	SentAt    time.Time `json:"sent_at"`
}

func NewPNRPAccountFailureEmailNotifier(
	accountRepo AccountRepository,
	opsService *OpsService,
	emailService *EmailService,
	settingRepo SettingRepository,
	configSvc *PNRPAccountAlertConfigService,
) *PNRPAccountFailureEmailNotifier {
	return &PNRPAccountFailureEmailNotifier{
		accountRepo:  accountRepo,
		opsService:   opsService,
		emailService: emailService,
		settingRepo:  settingRepo,
		configSvc:    configSvc,
	}
}

func (n *PNRPAccountFailureEmailNotifier) NotifyScheduledTestFailure(ctx context.Context, plan *ScheduledTestPlan, result *ScheduledTestResult) {
	if n == nil || n.emailService == nil || plan == nil || result == nil || result.Status == "success" {
		return
	}

	accountID := plan.AccountID
	planID := plan.ID
	modelID := strings.TrimSpace(plan.ModelID)
	errorMessage := strings.TrimSpace(result.ErrorMessage)
	finishedAt := result.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now()
	}
	if errorMessage == "" {
		errorMessage = "scheduled account test failed"
	}

	go n.sendScheduledTestFailureEmail(accountID, planID, modelID, errorMessage, finishedAt)
}

func (n *PNRPAccountFailureEmailNotifier) sendScheduledTestFailureEmail(accountID int64, planID int64, modelID string, errorMessage string, failedAt time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	cfg := n.resolveConfig(ctx)
	if !cfg.Enabled || !cfg.ScheduledTestFailureEnabled {
		cancel()
		return
	}
	account := n.getAccount(ctx, accountID)
	if pnrpIsRateLimitFailure(account, errorMessage) {
		if !cfg.RateLimitFailureEnabled {
			cancel()
			return
		}
	} else if !cfg.ErrorFailureEnabled {
		cancel()
		return
	}

	recipients := n.resolveRecipients(ctx, cfg)
	cancel()
	if len(recipients) == 0 {
		return
	}

	errorHash := pnrpAccountFailureHash(accountID, modelID, errorMessage)
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	stateKey := pnrpAccountAlertSettingKey(pnrpAccountAlertKindScheduledTest, strconv.FormatInt(accountID, 10))
	if !n.shouldSend(ctx, stateKey, errorHash, time.Now().UTC(), cfg.Cooldown()) {
		cancel()
		return
	}
	cancel()

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	siteName := n.siteName(ctx)
	cancel()
	alertTitle := pnrpAccountAlertTitle(account, errorMessage)
	subject := fmt.Sprintf("[%s] %s - %s", sanitizeEmailHeader(siteName), sanitizeEmailHeader(alertTitle), sanitizeEmailHeader(pnrpAccountDisplayName(account, accountID)))
	body := n.buildEmailBody(siteName, alertTitle, account, accountID, planID, modelID, errorMessage, failedAt, cfg.CooldownMinutes)

	anySent := false
	for _, to := range recipients {
		sendCtx, sendCancel := context.WithTimeout(context.Background(), emailSendTimeout)
		err := n.emailService.SendEmail(sendCtx, to, subject, body)
		sendCancel()
		if err != nil {
			slog.Warn("pnrp account failure email send failed", "account_id", accountID, "recipient_hash", notificationEmailHash(to), "error", err)
			continue
		}
		anySent = true
	}

	if anySent {
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		n.rememberSent(ctx, stateKey, errorHash, time.Now().UTC())
		cancel()
	}
}

func (n *PNRPAccountFailureEmailNotifier) NotifyAvailableAccountThreshold(ctx context.Context) {
	if n == nil || n.emailService == nil || n.accountRepo == nil {
		return
	}

	cfg := n.resolveConfig(ctx)
	if !cfg.Enabled || !cfg.MinAvailableAccountsEnabled {
		return
	}

	availableAccounts, err := n.accountRepo.ListSchedulable(ctx)
	if err != nil {
		slog.Warn("pnrp account alert availability load failed", "error", err)
		return
	}
	availableCount := len(availableAccounts)
	if availableCount >= cfg.MinAvailableAccounts {
		return
	}

	recipients := n.resolveRecipients(ctx, cfg)
	if len(recipients) == 0 {
		return
	}

	activeCount := 0
	if activeAccounts, err := n.accountRepo.ListActive(ctx); err == nil {
		activeCount = len(activeAccounts)
	}

	stateKey := pnrpAccountAlertSettingKey(pnrpAccountAlertKindAvailability, "global")
	errorHash := fmt.Sprintf("%d:%d:%d", availableCount, cfg.MinAvailableAccounts, activeCount)
	if !n.shouldSend(ctx, stateKey, errorHash, time.Now().UTC(), cfg.Cooldown()) {
		return
	}

	siteName := n.siteName(ctx)
	subject := fmt.Sprintf("[%s] 可用账号不足提醒 - %d/%d", sanitizeEmailHeader(siteName), availableCount, cfg.MinAvailableAccounts)
	body := n.buildAvailabilityEmailBody(siteName, availableCount, activeCount, cfg.MinAvailableAccounts, cfg.CooldownMinutes)

	anySent := false
	for _, to := range recipients {
		sendCtx, sendCancel := context.WithTimeout(context.Background(), emailSendTimeout)
		err := n.emailService.SendEmail(sendCtx, to, subject, body)
		sendCancel()
		if err != nil {
			slog.Warn("pnrp account availability email send failed", "recipient_hash", notificationEmailHash(to), "error", err)
			continue
		}
		anySent = true
	}

	if anySent {
		n.rememberSent(ctx, stateKey, errorHash, time.Now().UTC())
	}
}

func (n *PNRPAccountFailureEmailNotifier) resolveConfig(ctx context.Context) PNRPAccountAlertConfig {
	if n != nil && n.configSvc != nil {
		cfg, err := n.configSvc.GetConfig(ctx)
		if err == nil {
			return cfg
		}
		slog.Warn("pnrp account alert config load failed", "error", err)
	}
	return DefaultPNRPAccountAlertConfig()
}

func (n *PNRPAccountFailureEmailNotifier) resolveRecipients(ctx context.Context, cfg PNRPAccountAlertConfig) []string {
	var raw []string
	if cfg.UseOpsEmailRecipients && n.opsService != nil {
		if cfg, err := n.opsService.GetEmailNotificationConfig(ctx); err == nil && cfg != nil && cfg.Alert.Enabled {
			raw = append(raw, cfg.Alert.Recipients...)
		}
	}
	raw = append(raw, cfg.Recipients...)
	if len(raw) == 0 && cfg.UseOpsEmailRecipients && n.settingRepo != nil {
		if v, err := n.settingRepo.GetValue(ctx, SettingKeyAccountQuotaNotifyEmails); err == nil {
			raw = append(raw, filterVerifiedEmails(ParseNotifyEmails(v))...)
		}
	}
	return pnrpDeduplicateEmails(raw)
}

func (n *PNRPAccountFailureEmailNotifier) shouldSend(ctx context.Context, stateKey string, errorHash string, now time.Time, cooldown time.Duration) bool {
	if n.settingRepo == nil || strings.TrimSpace(stateKey) == "" {
		return true
	}
	raw, err := n.settingRepo.GetValue(ctx, stateKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return true
	}
	var state pnrpAccountFailureAlertState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return true
	}
	if state.ErrorHash != errorHash {
		return true
	}
	if state.SentAt.IsZero() {
		return true
	}
	return now.Sub(state.SentAt) >= cooldown
}

func (n *PNRPAccountFailureEmailNotifier) rememberSent(ctx context.Context, stateKey string, errorHash string, sentAt time.Time) {
	if n.settingRepo == nil || strings.TrimSpace(stateKey) == "" {
		return
	}
	data, err := json.Marshal(pnrpAccountFailureAlertState{ErrorHash: errorHash, SentAt: sentAt})
	if err != nil {
		return
	}
	if err := n.settingRepo.Set(ctx, stateKey, string(data)); err != nil {
		slog.Warn("pnrp account alert email state save failed", "state_key", stateKey, "error", err)
	}
}

func (n *PNRPAccountFailureEmailNotifier) getAccount(ctx context.Context, accountID int64) *Account {
	if n.accountRepo == nil || accountID <= 0 {
		return nil
	}
	account, err := n.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		slog.Warn("pnrp account failure email account load failed", "account_id", accountID, "error", err)
		return nil
	}
	return account
}

func (n *PNRPAccountFailureEmailNotifier) siteName(ctx context.Context) string {
	if n.settingRepo == nil {
		return defaultSiteName
	}
	name, err := n.settingRepo.GetValue(ctx, SettingKeySiteName)
	if err != nil || strings.TrimSpace(name) == "" {
		return defaultSiteName
	}
	return strings.TrimSpace(name)
}

func (n *PNRPAccountFailureEmailNotifier) buildEmailBody(siteName string, alertTitle string, account *Account, accountID int64, planID int64, modelID string, errorMessage string, failedAt time.Time, cooldownMinutes int) string {
	accountName := pnrpAccountDisplayName(account, accountID)
	platform := "-"
	status := "-"
	if account != nil {
		platform = strings.TrimSpace(account.Platform)
		status = strings.TrimSpace(account.Status)
	}
	if platform == "" {
		platform = "-"
	}
	if status == "" {
		status = "-"
	}
	if strings.TrimSpace(modelID) == "" {
		modelID = "默认模型"
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
</head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #f6f7fb; color: #111827; margin: 0; padding: 24px;">
  <div style="max-width: 640px; margin: 0 auto; background: #ffffff; border: 1px solid #e5e7eb; border-radius: 8px; overflow: hidden;">
    <div style="background: #dc2626; color: #ffffff; padding: 18px 22px;">
      <h2 style="margin: 0; font-size: 20px;">%s</h2>
    </div>
    <div style="padding: 22px;">
      <p>系统定时检测发现一个上游账号测试失败，请尽快登录后台检查账号状态。</p>
      <table style="width: 100%%; border-collapse: collapse; margin-top: 16px;">
        <tr><td style="padding: 8px 0; color: #6b7280;">站点</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">账号</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">账号 ID</td><td style="padding: 8px 0;">%d</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">平台</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">当前状态</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">检测计划 ID</td><td style="padding: 8px 0;">%d</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">检测模型</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">失败时间</td><td style="padding: 8px 0;">%s</td></tr>
      </table>
      <div style="margin-top: 18px; padding: 14px; background: #fef2f2; border: 1px solid #fecaca; border-radius: 6px;">
        <div style="font-weight: 600; margin-bottom: 8px;">错误原因</div>
        <div style="white-space: pre-wrap; word-break: break-word;">%s</div>
      </div>
    </div>
    <div style="padding: 14px 22px; background: #f9fafb; color: #6b7280; font-size: 12px;">
      该邮件由 PNRP 自定义账号检测告警发送。同类问题 %d 分钟内只提醒一次。
    </div>
  </div>
</body>
</html>`,
		htmlEscape(alertTitle),
		htmlEscape(siteName),
		htmlEscape(accountName),
		accountID,
		htmlEscape(platform),
		htmlEscape(status),
		planID,
		htmlEscape(modelID),
		htmlEscape(failedAt.Format(time.RFC3339)),
		htmlEscape(errorMessage),
		cooldownMinutes,
	)
}

func (n *PNRPAccountFailureEmailNotifier) buildAvailabilityEmailBody(siteName string, availableCount int, activeCount int, threshold int, cooldownMinutes int) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
</head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #f6f7fb; color: #111827; margin: 0; padding: 24px;">
  <div style="max-width: 640px; margin: 0 auto; background: #ffffff; border: 1px solid #e5e7eb; border-radius: 8px; overflow: hidden;">
    <div style="background: #d97706; color: #ffffff; padding: 18px 22px;">
      <h2 style="margin: 0; font-size: 20px;">可用账号不足提醒</h2>
    </div>
    <div style="padding: 22px;">
      <p>系统检测到当前可调度账号数量低于你设置的阈值，请尽快登录后台检查账号状态、限流和可调度开关。</p>
      <table style="width: 100%%; border-collapse: collapse; margin-top: 16px;">
        <tr><td style="padding: 8px 0; color: #6b7280;">站点</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">可调度账号数</td><td style="padding: 8px 0;">%d</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">活跃账号数</td><td style="padding: 8px 0;">%d</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">告警阈值</td><td style="padding: 8px 0;">%d</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">检测时间</td><td style="padding: 8px 0;">%s</td></tr>
      </table>
    </div>
    <div style="padding: 14px 22px; background: #f9fafb; color: #6b7280; font-size: 12px;">
      该邮件由 PNRP 自定义账号检测告警发送。同类问题 %d 分钟内只提醒一次。
    </div>
  </div>
</body>
</html>`,
		htmlEscape(siteName),
		availableCount,
		activeCount,
		threshold,
		htmlEscape(time.Now().UTC().Format(time.RFC3339)),
		cooldownMinutes,
	)
}

func pnrpAccountAlertSettingKey(kind string, id string) string {
	return pnrpAccountAlertSettingPrefix + strings.TrimSpace(kind) + ":" + strings.TrimSpace(id)
}

func pnrpAccountFailureHash(accountID int64, modelID string, errorMessage string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\n%s\n%s", accountID, strings.TrimSpace(modelID), strings.TrimSpace(errorMessage))))
	return hex.EncodeToString(sum[:])
}

func pnrpAccountDisplayName(account *Account, accountID int64) string {
	if account != nil && strings.TrimSpace(account.Name) != "" {
		return strings.TrimSpace(account.Name)
	}
	return "account-" + strconv.FormatInt(accountID, 10)
}

func pnrpIsRateLimitFailure(account *Account, errorMessage string) bool {
	if account != nil {
		if account.IsRateLimited() {
			return true
		}
	}
	msg := strings.ToLower(strings.TrimSpace(errorMessage))
	return strings.Contains(msg, "rate_limit") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "rate-limited") ||
		strings.Contains(msg, "rate limited") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "status 429") ||
		strings.Contains(msg, " 429 ") ||
		strings.Contains(msg, "code 429")
}

func pnrpAccountAlertTitle(account *Account, errorMessage string) string {
	if pnrpIsRateLimitFailure(account, errorMessage) {
		return "账号限流提醒"
	}
	if account != nil && strings.TrimSpace(account.Status) == StatusError {
		return "账号错误提醒"
	}
	return "账号不可用提醒"
}

func pnrpDeduplicateEmails(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		email := strings.TrimSpace(item)
		if email == "" {
			continue
		}
		key := strings.ToLower(email)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, email)
	}
	return out
}
