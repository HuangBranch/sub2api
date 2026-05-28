package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	pnrpAccountAlertSettingPrefix = "pnrp_account_alert:"
)

const (
	pnrpAccountAlertKindScheduledTest = "scheduled_test"
	pnrpAccountAlertKindAvailability  = "availability"
	pnrpAccountAlertKindAccountLimit  = "account_limit"
	pnrpAccountAlertKindAccountError  = "account_error"
)

// ScheduledTestFailureNotifier is a narrow hook used by the scheduled test runner.
// PNRP custom alerting lives behind this interface so the upstream runner only
// needs a small optional call site.
type ScheduledTestFailureNotifier interface {
	NotifyScheduledTestFailure(ctx context.Context, plan *ScheduledTestPlan, result *ScheduledTestResult)
}

type PNRPAccountFailureEmailNotifier struct {
	accountRepo  AccountRepository
	groupRepo    GroupRepository
	opsService   *OpsService
	emailService *EmailService
	settingRepo  SettingRepository
	configSvc    *PNRPAccountAlertConfigService
}

type pnrpAccountFailureAlertState struct {
	ErrorHash string    `json:"error_hash"`
	SentAt    time.Time `json:"sent_at"`
}

type PNRPAccountAlertCheckSummary struct {
	Enabled                     bool      `json:"enabled"`
	Manual                      bool      `json:"manual"`
	StartedAt                   time.Time `json:"started_at"`
	FinishedAt                  time.Time `json:"finished_at"`
	Recipients                  int       `json:"recipients"`
	AccountsChecked             int       `json:"accounts_checked"`
	LimitedAccounts             int       `json:"limited_accounts"`
	ErrorAccounts               int       `json:"error_accounts"`
	GroupsChecked               int       `json:"groups_checked"`
	EmailsSent                  int       `json:"emails_sent"`
	EmailsFailed                int       `json:"emails_failed"`
	LimitAlertsSent             int       `json:"limit_alerts_sent"`
	LimitRecoveryAlertsSent     int       `json:"limit_recovery_alerts_sent"`
	ErrorAlertsSent             int       `json:"error_alerts_sent"`
	AvailabilityAlertsSent      int       `json:"availability_alerts_sent"`
	AvailabilityAlertsRecovered int       `json:"availability_alerts_recovered"`
}

type pnrpAccountAlertEmailResult struct {
	Sent   int
	Failed int
}

type pnrpAccountLimitIssue struct {
	Reason    string
	Detail    string
	Until     string
	Signature string
}

func NewPNRPAccountFailureEmailNotifier(
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	opsService *OpsService,
	emailService *EmailService,
	settingRepo SettingRepository,
	configSvc *PNRPAccountAlertConfigService,
) *PNRPAccountFailureEmailNotifier {
	return &PNRPAccountFailureEmailNotifier{
		accountRepo:  accountRepo,
		groupRepo:    groupRepo,
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

	result := n.sendAlertEmail(recipients, subject, body, "scheduled_test_failure", "account_id", accountID, "plan_id", planID)
	if result.Sent > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		n.rememberSent(ctx, stateKey, errorHash, time.Now().UTC())
		cancel()
	}
}

func (n *PNRPAccountFailureEmailNotifier) RunAccountAlertCheck(ctx context.Context, manual bool) (PNRPAccountAlertCheckSummary, error) {
	startedAt := time.Now().UTC()
	summary := PNRPAccountAlertCheckSummary{
		Manual:    manual,
		StartedAt: startedAt,
	}

	if n == nil || n.emailService == nil || n.accountRepo == nil {
		summary.FinishedAt = time.Now().UTC()
		return summary, nil
	}

	cfg := n.resolveConfig(ctx)
	summary.Enabled = cfg.Enabled
	if !cfg.Enabled {
		summary.FinishedAt = time.Now().UTC()
		return summary, nil
	}

	recipients := n.resolveRecipients(ctx, cfg)
	summary.Recipients = len(recipients)
	if len(recipients) == 0 {
		slog.Warn("pnrp account alert check skipped: no recipients configured")
		summary.FinishedAt = time.Now().UTC()
		return summary, nil
	}

	siteName := n.siteName(ctx)
	accounts, err := n.loadAllAccounts(ctx)
	if err != nil {
		summary.FinishedAt = time.Now().UTC()
		return summary, err
	}
	summary.AccountsChecked = len(accounts)

	now := time.Now().UTC()
	if cfg.RateLimitFailureEnabled {
		n.checkAccountLimitAlerts(ctx, accounts, recipients, siteName, now, &summary)
	}
	if cfg.ErrorFailureEnabled {
		n.checkAccountErrorAlerts(ctx, accounts, recipients, siteName, now, &summary)
	}
	if cfg.MinAvailableAccountsEnabled {
		n.checkAvailabilityAlerts(ctx, recipients, siteName, now, cfg.MinAvailableAccounts, &summary)
	}

	summary.FinishedAt = time.Now().UTC()
	slog.Info("pnrp account alert check completed",
		"manual", manual,
		"accounts_checked", summary.AccountsChecked,
		"limited_accounts", summary.LimitedAccounts,
		"error_accounts", summary.ErrorAccounts,
		"groups_checked", summary.GroupsChecked,
		"emails_sent", summary.EmailsSent,
		"emails_failed", summary.EmailsFailed,
	)
	return summary, nil
}

func (n *PNRPAccountFailureEmailNotifier) NotifyAvailableAccountThreshold(ctx context.Context) {
	summary, err := n.RunAccountAlertCheck(ctx, false)
	if err != nil {
		slog.Warn("pnrp account alert check failed", "error", err)
		return
	}
	slog.Debug("pnrp account alert threshold check completed", "emails_sent", summary.EmailsSent)
}

func (n *PNRPAccountFailureEmailNotifier) loadAllAccounts(ctx context.Context) ([]Account, error) {
	const pageSize = 1000

	out := make([]Account, 0, pageSize)
	for page := 1; ; page++ {
		accounts, result, err := n.accountRepo.List(ctx, pagination.PaginationParams{
			Page:      page,
			PageSize:  pageSize,
			SortBy:    "id",
			SortOrder: pagination.SortOrderAsc,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, accounts...)
		if len(accounts) == 0 || result == nil || page >= result.Pages {
			break
		}
	}
	return out, nil
}

func (n *PNRPAccountFailureEmailNotifier) checkAccountLimitAlerts(ctx context.Context, accounts []Account, recipients []string, siteName string, now time.Time, summary *PNRPAccountAlertCheckSummary) {
	for i := range accounts {
		account := &accounts[i]
		if account.ID <= 0 {
			continue
		}
		stateKey := pnrpAccountAlertSettingKey(pnrpAccountAlertKindAccountLimit, strconv.FormatInt(account.ID, 10))
		issue, limited := pnrpDetectAccountLimit(account, now)
		if limited {
			summary.LimitedAccounts++
			if _, ok := n.loadAlertState(ctx, stateKey); ok {
				continue
			}

			subject := fmt.Sprintf("[%s] 账号限制提醒 - %s", sanitizeEmailHeader(siteName), sanitizeEmailHeader(pnrpAccountDisplayName(account, account.ID)))
			body := n.buildAccountLimitEmailBody(siteName, account, issue, now)
			result := n.sendAlertEmail(recipients, subject, body, "account_limit", "account_id", account.ID)
			mergePNRPEmailResult(summary, result)
			if result.Sent > 0 {
				summary.LimitAlertsSent++
				n.rememberSent(ctx, stateKey, issue.Signature, now)
			}
			continue
		}

		if _, ok := n.loadAlertState(ctx, stateKey); ok {
			subject := fmt.Sprintf("[%s] 账号限制恢复 - %s", sanitizeEmailHeader(siteName), sanitizeEmailHeader(pnrpAccountDisplayName(account, account.ID)))
			body := n.buildAccountLimitRecoveryEmailBody(siteName, account, now)
			result := n.sendAlertEmail(recipients, subject, body, "account_limit_recovered", "account_id", account.ID)
			mergePNRPEmailResult(summary, result)
			if result.Sent > 0 {
				summary.LimitRecoveryAlertsSent++
				n.forgetSent(ctx, stateKey)
			}
		}
	}
}

func (n *PNRPAccountFailureEmailNotifier) checkAccountErrorAlerts(ctx context.Context, accounts []Account, recipients []string, siteName string, now time.Time, summary *PNRPAccountAlertCheckSummary) {
	for i := range accounts {
		account := &accounts[i]
		if account.ID <= 0 {
			continue
		}
		stateKey := pnrpAccountAlertSettingKey(pnrpAccountAlertKindAccountError, strconv.FormatInt(account.ID, 10))
		if strings.TrimSpace(account.Status) != StatusError {
			if _, ok := n.loadAlertState(ctx, stateKey); ok {
				n.forgetSent(ctx, stateKey)
			}
			continue
		}

		summary.ErrorAccounts++
		errorMessage := strings.TrimSpace(account.ErrorMessage)
		if errorMessage == "" {
			errorMessage = "account entered error status"
		}
		errorHash := pnrpAccountFailureHash(account.ID, "status_error", errorMessage)
		if n.stateMatches(ctx, stateKey, errorHash) {
			continue
		}

		subject := fmt.Sprintf("[%s] 账号错误提醒 - %s", sanitizeEmailHeader(siteName), sanitizeEmailHeader(pnrpAccountDisplayName(account, account.ID)))
		body := n.buildAccountErrorEmailBody(siteName, account, errorMessage, now)
		result := n.sendAlertEmail(recipients, subject, body, "account_error", "account_id", account.ID)
		mergePNRPEmailResult(summary, result)
		if result.Sent > 0 {
			summary.ErrorAlertsSent++
			n.rememberSent(ctx, stateKey, errorHash, now)
		}
	}
}

func (n *PNRPAccountFailureEmailNotifier) checkAvailabilityAlerts(ctx context.Context, recipients []string, siteName string, now time.Time, threshold int, summary *PNRPAccountAlertCheckSummary) {
	availableAccounts, err := n.accountRepo.ListSchedulable(ctx)
	if err != nil {
		slog.Warn("pnrp account alert availability load failed", "error", err)
		return
	}
	availableCount := len(availableAccounts)
	activeCount := 0
	if activeAccounts, err := n.accountRepo.ListActive(ctx); err == nil {
		activeCount = len(activeAccounts)
	}
	n.handleAvailabilityState(ctx, recipients, siteName, now, summary, "global", "全局账号池", availableCount, activeCount, threshold)

	if n.groupRepo == nil {
		return
	}
	groups, err := n.groupRepo.ListActive(ctx)
	if err != nil {
		slog.Warn("pnrp account alert group availability load failed", "error", err)
		return
	}
	for i := range groups {
		group := &groups[i]
		if group.ID <= 0 || group.AccountCount <= 0 {
			continue
		}
		summary.GroupsChecked++
		scopeID := "group:" + strconv.FormatInt(group.ID, 10)
		scopeName := fmt.Sprintf("分组：%s", pnrpGroupDisplayName(group))
		n.handleAvailabilityState(ctx, recipients, siteName, now, summary, scopeID, scopeName, int(group.ActiveAccountCount), int(group.AccountCount), threshold)
	}
}

func (n *PNRPAccountFailureEmailNotifier) handleAvailabilityState(ctx context.Context, recipients []string, siteName string, now time.Time, summary *PNRPAccountAlertCheckSummary, scopeID string, scopeName string, availableCount int, totalCount int, threshold int) {
	stateKey := pnrpAccountAlertSettingKey(pnrpAccountAlertKindAvailability, scopeID)
	if availableCount >= threshold {
		if _, ok := n.loadAlertState(ctx, stateKey); ok {
			summary.AvailabilityAlertsRecovered++
			n.forgetSent(ctx, stateKey)
		}
		return
	}

	const availabilitySignature = "below-threshold"
	if n.stateMatches(ctx, stateKey, availabilitySignature) {
		return
	}

	subject := fmt.Sprintf("[%s] 可用账号不足提醒 - %s %d/%d", sanitizeEmailHeader(siteName), sanitizeEmailHeader(scopeName), availableCount, threshold)
	body := n.buildAvailabilityEmailBody(siteName, scopeName, availableCount, totalCount, threshold, now)
	result := n.sendAlertEmail(recipients, subject, body, "account_availability", "scope", scopeID)
	mergePNRPEmailResult(summary, result)
	if result.Sent > 0 {
		summary.AvailabilityAlertsSent++
		n.rememberSent(ctx, stateKey, availabilitySignature, now)
	}
}

func (n *PNRPAccountFailureEmailNotifier) sendAlertEmail(recipients []string, subject string, body string, event string, attrs ...any) pnrpAccountAlertEmailResult {
	var result pnrpAccountAlertEmailResult
	for _, to := range recipients {
		sendCtx, sendCancel := context.WithTimeout(context.Background(), emailSendTimeout)
		err := n.emailService.SendEmail(sendCtx, to, subject, body)
		sendCancel()

		logAttrs := make([]any, 0, len(attrs)+4)
		logAttrs = append(logAttrs, "event", event, "recipient_hash", notificationEmailHash(to))
		logAttrs = append(logAttrs, attrs...)
		if err != nil {
			result.Failed++
			logAttrs = append(logAttrs, "error", err)
			slog.Warn("pnrp account alert email send failed", logAttrs...)
			continue
		}
		result.Sent++
		slog.Info("pnrp account alert email sent", logAttrs...)
	}
	return result
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
	state, ok := n.loadAlertState(ctx, stateKey)
	if !ok {
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

func (n *PNRPAccountFailureEmailNotifier) stateMatches(ctx context.Context, stateKey string, errorHash string) bool {
	if n.settingRepo == nil || strings.TrimSpace(stateKey) == "" {
		return false
	}
	state, ok := n.loadAlertState(ctx, stateKey)
	if !ok {
		return false
	}
	return state.ErrorHash == errorHash
}

func (n *PNRPAccountFailureEmailNotifier) loadAlertState(ctx context.Context, stateKey string) (pnrpAccountFailureAlertState, bool) {
	var state pnrpAccountFailureAlertState
	if n.settingRepo == nil || strings.TrimSpace(stateKey) == "" {
		return state, false
	}
	raw, err := n.settingRepo.GetValue(ctx, stateKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return state, false
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return state, false
	}
	return state, true
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

func (n *PNRPAccountFailureEmailNotifier) forgetSent(ctx context.Context, stateKey string) {
	if n.settingRepo == nil || strings.TrimSpace(stateKey) == "" {
		return
	}
	if err := n.settingRepo.Delete(ctx, stateKey); err != nil {
		slog.Warn("pnrp account alert email state delete failed", "state_key", stateKey, "error", err)
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

func (n *PNRPAccountFailureEmailNotifier) buildAccountLimitEmailBody(siteName string, account *Account, issue pnrpAccountLimitIssue, detectedAt time.Time) string {
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	}
	return n.buildAccountAlertEmailBody(
		siteName,
		"账号限制提醒",
		"#d97706",
		"系统检测到账号处于限流、过载或临时不可调度状态。此类限制提醒同一个限制周期只发送一次；限制解除后会再发送恢复提醒。",
		account,
		map[string]string{
			"限制类型":   issue.Reason,
			"限制详情":   issue.Detail,
			"预计恢复时间": issue.Until,
			"检测时间":   detectedAt.Format(time.RFC3339),
		},
		fmt.Sprintf("账号 ID %d 当前限制：%s", accountID, issue.Detail),
	)
}

func (n *PNRPAccountFailureEmailNotifier) buildAccountLimitRecoveryEmailBody(siteName string, account *Account, detectedAt time.Time) string {
	return n.buildAccountAlertEmailBody(
		siteName,
		"账号限制恢复",
		"#059669",
		"系统检测到账号的限流、过载或临时不可调度状态已经解除，账号已重新进入可用状态判断流程。",
		account,
		map[string]string{
			"恢复状态": "当前未检测到限流、过载或临时不可调度窗口",
			"检测时间": detectedAt.Format(time.RFC3339),
		},
		"账号限制状态已恢复。",
	)
}

func (n *PNRPAccountFailureEmailNotifier) buildAccountErrorEmailBody(siteName string, account *Account, errorMessage string, detectedAt time.Time) string {
	return n.buildAccountAlertEmailBody(
		siteName,
		"账号错误提醒",
		"#dc2626",
		"系统检测到账号已经进入错误状态。错误账号通常会自动退出调度，请尽快登录后台处理。",
		account,
		map[string]string{
			"错误原因": errorMessage,
			"检测时间": detectedAt.Format(time.RFC3339),
		},
		errorMessage,
	)
}

func (n *PNRPAccountFailureEmailNotifier) buildAccountAlertEmailBody(siteName string, title string, color string, intro string, account *Account, extra map[string]string, detail string) string {
	accountID := int64(0)
	accountName := "-"
	platform := "-"
	accountType := "-"
	status := "-"
	schedulable := "-"
	groups := "-"
	if account != nil {
		accountID = account.ID
		accountName = pnrpAccountDisplayName(account, account.ID)
		platform = dashIfEmpty(account.Platform)
		accountType = dashIfEmpty(account.Type)
		status = dashIfEmpty(account.Status)
		schedulable = strconv.FormatBool(account.Schedulable)
		groups = pnrpAccountGroupNames(account)
	}

	extraRows := ""
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		extraRows += fmt.Sprintf(`<tr><td style="padding: 8px 0; color: #6b7280;">%s</td><td style="padding: 8px 0;">%s</td></tr>`, htmlEscape(key), htmlEscape(extra[key]))
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
</head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #f6f7fb; color: #111827; margin: 0; padding: 24px;">
  <div style="max-width: 680px; margin: 0 auto; background: #ffffff; border: 1px solid #e5e7eb; border-radius: 8px; overflow: hidden;">
    <div style="background: %s; color: #ffffff; padding: 18px 22px;">
      <h2 style="margin: 0; font-size: 20px;">%s</h2>
    </div>
    <div style="padding: 22px;">
      <p>%s</p>
      <table style="width: 100%%; border-collapse: collapse; margin-top: 16px;">
        <tr><td style="padding: 8px 0; color: #6b7280;">站点</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">账号</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">账号 ID</td><td style="padding: 8px 0;">%d</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">平台</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">类型</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">当前状态</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">可调度开关</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">所属分组</td><td style="padding: 8px 0;">%s</td></tr>
        %s
      </table>
      <div style="margin-top: 18px; padding: 14px; background: #f9fafb; border: 1px solid #e5e7eb; border-radius: 6px;">
        <div style="font-weight: 600; margin-bottom: 8px;">详情</div>
        <div style="white-space: pre-wrap; word-break: break-word;">%s</div>
      </div>
    </div>
    <div style="padding: 14px 22px; background: #f9fafb; color: #6b7280; font-size: 12px;">
      该邮件由 PNRP 自定义账号检测告警发送。
    </div>
  </div>
</body>
</html>`,
		htmlEscape(color),
		htmlEscape(title),
		htmlEscape(intro),
		htmlEscape(siteName),
		htmlEscape(accountName),
		accountID,
		htmlEscape(platform),
		htmlEscape(accountType),
		htmlEscape(status),
		htmlEscape(schedulable),
		htmlEscape(groups),
		extraRows,
		htmlEscape(detail),
	)
}

func (n *PNRPAccountFailureEmailNotifier) buildAvailabilityEmailBody(siteName string, scopeName string, availableCount int, totalCount int, threshold int, detectedAt time.Time) string {
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
        <tr><td style="padding: 8px 0; color: #6b7280;">范围</td><td style="padding: 8px 0;">%s</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">可调度账号数</td><td style="padding: 8px 0;">%d</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">绑定/活跃账号数</td><td style="padding: 8px 0;">%d</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">告警阈值</td><td style="padding: 8px 0;">%d</td></tr>
        <tr><td style="padding: 8px 0; color: #6b7280;">检测时间</td><td style="padding: 8px 0;">%s</td></tr>
      </table>
    </div>
    <div style="padding: 14px 22px; background: #f9fafb; color: #6b7280; font-size: 12px;">
      该邮件由 PNRP 自定义账号检测告警发送。低于阈值时只提醒一次，恢复后会自动解除去重状态。
    </div>
  </div>
</body>
</html>`,
		htmlEscape(siteName),
		htmlEscape(scopeName),
		availableCount,
		totalCount,
		threshold,
		htmlEscape(detectedAt.Format(time.RFC3339)),
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

func pnrpDetectAccountLimit(account *Account, now time.Time) (pnrpAccountLimitIssue, bool) {
	if account == nil {
		return pnrpAccountLimitIssue{}, false
	}
	parts := make([]string, 0, 3)
	if account.RateLimitResetAt != nil && now.Before(account.RateLimitResetAt.UTC()) {
		until := account.RateLimitResetAt.UTC().Format(time.RFC3339)
		parts = append(parts, "rate_limit:"+until)
		return pnrpAccountLimitIssue{
			Reason:    "限流",
			Detail:    "账号触发 rate limit，调度会等到重置时间后再尝试使用。",
			Until:     until,
			Signature: strings.Join(parts, "|"),
		}, true
	}
	if account.OverloadUntil != nil && now.Before(account.OverloadUntil.UTC()) {
		until := account.OverloadUntil.UTC().Format(time.RFC3339)
		parts = append(parts, "overload:"+until)
		return pnrpAccountLimitIssue{
			Reason:    "过载保护",
			Detail:    "账号处于过载保护窗口，调度会暂时跳过该账号。",
			Until:     until,
			Signature: strings.Join(parts, "|"),
		}, true
	}
	if account.TempUnschedulableUntil != nil && now.Before(account.TempUnschedulableUntil.UTC()) {
		until := account.TempUnschedulableUntil.UTC().Format(time.RFC3339)
		reason := strings.TrimSpace(account.TempUnschedulableReason)
		if reason == "" {
			reason = "临时不可调度"
		}
		parts = append(parts, "temp_unschedulable:"+until+":"+reason)
		return pnrpAccountLimitIssue{
			Reason:    "临时不可调度",
			Detail:    reason,
			Until:     until,
			Signature: strings.Join(parts, "|"),
		}, true
	}
	return pnrpAccountLimitIssue{}, false
}

func pnrpAccountGroupNames(account *Account) string {
	if account == nil {
		return "-"
	}
	names := make([]string, 0, len(account.Groups)+len(account.GroupIDs))
	seen := make(map[string]struct{}, len(account.Groups)+len(account.GroupIDs))
	for _, group := range account.Groups {
		if group == nil {
			continue
		}
		name := pnrpGroupDisplayName(group)
		key := "name:" + strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	for _, id := range account.GroupIDs {
		if id <= 0 {
			continue
		}
		key := "id:" + strconv.FormatInt(id, 10)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, "group-"+strconv.FormatInt(id, 10))
	}
	if len(names) == 0 {
		return "未绑定分组"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func pnrpGroupDisplayName(group *Group) string {
	if group != nil && strings.TrimSpace(group.Name) != "" {
		return strings.TrimSpace(group.Name)
	}
	if group != nil && group.ID > 0 {
		return "group-" + strconv.FormatInt(group.ID, 10)
	}
	return "未命名分组"
}

func dashIfEmpty(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func mergePNRPEmailResult(summary *PNRPAccountAlertCheckSummary, result pnrpAccountAlertEmailResult) {
	if summary == nil {
		return
	}
	summary.EmailsSent += result.Sent
	summary.EmailsFailed += result.Failed
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
