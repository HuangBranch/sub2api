//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPNRPAccountAlertFormatBeijingTime(t *testing.T) {
	value := time.Date(2026, 5, 28, 5, 37, 0, 0, time.UTC)

	got := pnrpAccountAlertFormatBeijingTime(value)

	require.Equal(t, "2026-05-28 13:37:00 北京时间 (UTC+08:00)", got)
}

func TestPNRPDetectAccountLimitUsesBeijingDisplayAndUTCSignature(t *testing.T) {
	now := time.Date(2026, 5, 28, 5, 37, 0, 0, time.UTC)
	resetAt := now.Add(2 * time.Minute)
	account := &Account{ID: 1, RateLimitResetAt: &resetAt}

	issue, limited := pnrpDetectAccountLimit(account, now)

	require.True(t, limited)
	require.Equal(t, "限流", issue.Reason)
	require.Equal(t, resetAt, issue.UntilAt)
	require.Equal(t, "2026-05-28 13:39:00 北京时间 (UTC+08:00)", issue.Until)
	require.Equal(t, "rate_limit:2026-05-28T05:39:00Z", issue.Signature)
}

func TestPNRPAccountAlertRecoverySoonWindow(t *testing.T) {
	now := time.Date(2026, 5, 28, 5, 37, 0, 0, time.UTC)
	account := &Account{ID: 1}

	outsideWindow := pnrpAccountLimitIssue{UntilAt: now.Add(2*time.Minute + time.Second)}
	require.False(t, pnrpAccountLimitRecoverySoonDue(account, outsideWindow, now))

	insideWindow := pnrpAccountLimitIssue{UntilAt: now.Add(2 * time.Minute)}
	require.True(t, pnrpAccountLimitRecoverySoonDue(account, insideWindow, now))

	expired := pnrpAccountLimitIssue{UntilAt: now.Add(-time.Second)}
	require.False(t, pnrpAccountLimitRecoverySoonDue(account, expired, now))

	require.False(t, pnrpAccountLimitRecoverySoonDue(nil, insideWindow, now))
}

func TestPNRPAccountAlertFormatDuration(t *testing.T) {
	require.Equal(t, "30 秒", pnrpAccountAlertFormatDuration(30*time.Second))
	require.Equal(t, "1 分钟", pnrpAccountAlertFormatDuration(time.Minute))
	require.Equal(t, "1 分 15 秒", pnrpAccountAlertFormatDuration(75*time.Second))
}
