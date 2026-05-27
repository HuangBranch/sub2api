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
	pnrpAccountFailureAlertCooldown = time.Hour
	pnrpAccountFailureSettingPrefix = "pnrp_account_failure_alert:"
)

// ScheduledTestFailureNotifier is a narrow hook used by the scheduled test runner.
// PNRP custom alerting lives behind this interface so the upstream runner only
// needs a small optional call site.
type ScheduledTestFailureNotifier interface {
	NotifyScheduledTestFailure(ctx context.Context, plan *ScheduledTestPlan, result *ScheduledTestResult)
}

type PNRPAccountFailureEmailNotifier struct {
	accountRepo AccountRepository
	opsService  *OpsService
	emailService *EmailService
	settingRepo  SettingRepository
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
) *PNRPAccountFailureEmailNotifier {
	return &PNRPAccountFailureEmailNotifier{
		accountRepo: accountRepo,
		opsService:  opsService,
		emailService: emailService,
		settingRepo:  settingRepo,
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

	recipients := n.resolveRecipients(ctx)
	cancel()
	if len(recipients) == 0 {
		return
	}

	errorHash := pnrpAccountFailureHash(accountID, modelID, errorMessage)
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	if !n.shouldSend(ctx, accountID, errorHash, time.Now().UTC()) {
		cancel()
		return
	}
	cancel()

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	account := n.getAccount(ctx, accountID)
	siteName := n.siteName(ctx)
	cancel()
	subject := fmt.Sprintf("[%s] 账号不可用提醒 - %s", sanitizeEmailHeader(siteName), sanitizeEmailHeader(pnrpAccountDisplayName(account, accountID)))
	body := n.buildEmailBody(siteName, account, accountID, planID, modelID, errorMessage, failedAt)

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
		n.rememberSent(ctx, accountID, errorHash, time.Now().UTC())
		cancel()
	}
}

func (n *PNRPAccountFailureEmailNotifier) resolveRecipients(ctx context.Context) []string {
	var raw []string
	if n.opsService != nil {
		if cfg, err := n.opsService.GetEmailNotificationConfig(ctx); err == nil && cfg != nil && cfg.Alert.Enabled {
			raw = append(raw, cfg.Alert.Recipients...)
		}
	}
	if len(raw) == 0 && n.settingRepo != nil {
		if v, err := n.settingRepo.GetValue(ctx, SettingKeyAccountQuotaNotifyEmails); err == nil {
			raw = append(raw, filterVerifiedEmails(ParseNotifyEmails(v))...)
		}
	}
	return pnrpDeduplicateEmails(raw)
}

func (n *PNRPAccountFailureEmailNotifier) shouldSend(ctx context.Context, accountID int64, errorHash string, now time.Time) bool {
	if n.settingRepo == nil || accountID <= 0 {
		return true
	}
	raw, err := n.settingRepo.GetValue(ctx, pnrpAccountFailureSettingKey(accountID))
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
	return now.Sub(state.SentAt) >= pnrpAccountFailureAlertCooldown
}

func (n *PNRPAccountFailureEmailNotifier) rememberSent(ctx context.Context, accountID int64, errorHash string, sentAt time.Time) {
	if n.settingRepo == nil || accountID <= 0 {
		return
	}
	data, err := json.Marshal(pnrpAccountFailureAlertState{ErrorHash: errorHash, SentAt: sentAt})
	if err != nil {
		return
	}
	if err := n.settingRepo.Set(ctx, pnrpAccountFailureSettingKey(accountID), string(data)); err != nil {
		slog.Warn("pnrp account failure email state save failed", "account_id", accountID, "error", err)
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

func (n *PNRPAccountFailureEmailNotifier) buildEmailBody(siteName string, account *Account, accountID int64, planID int64, modelID string, errorMessage string, failedAt time.Time) string {
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
      <h2 style="margin: 0; font-size: 20px;">账号不可用提醒</h2>
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
      该邮件由 PNRP 自定义账号检测告警发送。同一账号相同错误 1 小时内只提醒一次。
    </div>
  </div>
</body>
</html>`,
		htmlEscape(siteName),
		htmlEscape(accountName),
		accountID,
		htmlEscape(platform),
		htmlEscape(status),
		planID,
		htmlEscape(modelID),
		htmlEscape(failedAt.Format(time.RFC3339)),
		htmlEscape(errorMessage),
	)
}

func pnrpAccountFailureSettingKey(accountID int64) string {
	return pnrpAccountFailureSettingPrefix + strconv.FormatInt(accountID, 10)
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
