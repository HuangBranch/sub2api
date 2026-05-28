package service

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const pnrpAccountAlertScanInterval = 30 * time.Second

type PNRPAccountAlertHealthChecker struct {
	notifier *PNRPAccountFailureEmailNotifier

	startOnce           sync.Once
	stopOnce            sync.Once
	stopCh              chan struct{}
	lastAvailabilityRun time.Time
}

func NewPNRPAccountAlertHealthChecker(notifier *PNRPAccountFailureEmailNotifier) *PNRPAccountAlertHealthChecker {
	return &PNRPAccountAlertHealthChecker{
		notifier: notifier,
		stopCh:   make(chan struct{}),
	}
}

func ProvidePNRPAccountAlertHealthChecker(notifier *PNRPAccountFailureEmailNotifier) *PNRPAccountAlertHealthChecker {
	checker := NewPNRPAccountAlertHealthChecker(notifier)
	checker.Start()
	return checker
}

func (c *PNRPAccountAlertHealthChecker) Start() {
	if c == nil {
		return
	}
	c.startOnce.Do(func() {
		go c.loop()
	})
}

func (c *PNRPAccountAlertHealthChecker) Stop() {
	if c == nil {
		return
	}
	c.stopOnce.Do(func() {
		close(c.stopCh)
	})
}

func (c *PNRPAccountAlertHealthChecker) loop() {
	ticker := time.NewTicker(pnrpAccountAlertScanInterval)
	defer ticker.Stop()

	c.runIfDue()
	for {
		select {
		case <-ticker.C:
			c.runIfDue()
		case <-c.stopCh:
			return
		}
	}
}

func (c *PNRPAccountAlertHealthChecker) runIfDue() {
	if c == nil || c.notifier == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	cfg := c.notifier.resolveConfig(ctx)
	if !cfg.Enabled {
		cancel()
		return
	}
	interval := cfg.AvailabilityCheckInterval()
	includeAvailability := c.lastAvailabilityRun.IsZero() || time.Since(c.lastAvailabilityRun) >= interval
	if includeAvailability {
		c.lastAvailabilityRun = time.Now()
	}
	if _, err := c.notifier.runAccountAlertCheck(ctx, false, includeAvailability); err != nil {
		slog.Warn("pnrp account alert check failed", "error", err)
	}
	cancel()

	slog.Debug("pnrp account alert check completed")
}
