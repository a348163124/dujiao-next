package service

import (
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
)

func TestGetUpstreamSyncConfigFallbackToYaml(t *testing.T) {
	repo := newMockSettingRepo()
	svc := NewSettingService(repo)

	cfg, err := svc.GetUpstreamSyncConfig("30m")
	if err != nil {
		t.Fatalf("GetUpstreamSyncConfig failed: %v", err)
	}
	if cfg.IntervalMinutes != 30 {
		t.Fatalf("expected interval=30 (from yaml fallback), got %d", cfg.IntervalMinutes)
	}
	if !cfg.PreOrderStockCheckEnabled {
		t.Fatalf("expected pre-order check enabled by default")
	}
}

func TestGetUpstreamSyncConfigReadsFromDB(t *testing.T) {
	repo := newMockSettingRepo()
	svc := NewSettingService(repo)

	_, err := svc.Update(constants.SettingKeyUpstreamSyncConfig, map[string]interface{}{
		"interval_minutes":              360,
		"pre_order_stock_check_enabled": false,
	})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}

	cfg, err := svc.GetUpstreamSyncConfig("5m")
	if err != nil {
		t.Fatalf("GetUpstreamSyncConfig failed: %v", err)
	}
	if cfg.IntervalMinutes != 360 {
		t.Fatalf("expected interval=360 (from DB), got %d", cfg.IntervalMinutes)
	}
	if cfg.PreOrderStockCheckEnabled {
		t.Fatalf("expected pre-order check disabled per DB setting")
	}
}

func TestUpdateUpstreamSyncConfigNormalizesBelowMinimum(t *testing.T) {
	repo := newMockSettingRepo()
	svc := NewSettingService(repo)

	result, err := svc.Update(constants.SettingKeyUpstreamSyncConfig, map[string]interface{}{
		"interval_minutes": 1, // < 5
	})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	assertSettingIntValue(t, result, "interval_minutes", upstreamSyncIntervalMinDefault)
}

func TestComputeFullSyncIntervalFloorsAt24h(t *testing.T) {
	repo := newMockSettingRepo()
	settingSvc := NewSettingService(repo)
	svc := &ProductMappingService{settingService: settingSvc}

	// 默认 5m 同步间隔 × 3 = 15m < 24h，期望落到 24h floor
	got := svc.computeFullSyncInterval()
	if got != fullSyncIntervalFloor {
		t.Fatalf("expected floor=24h, got %v", got)
	}
}

func TestComputeFullSyncIntervalScalesWithLongInterval(t *testing.T) {
	repo := newMockSettingRepo()
	settingSvc := NewSettingService(repo)
	if _, err := settingSvc.Update(constants.SettingKeyUpstreamSyncConfig, map[string]interface{}{
		"interval_minutes": 720, // 12h
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	svc := &ProductMappingService{settingService: settingSvc}

	// 12h * 3 = 36h，应使用 scaled 值
	got := svc.computeFullSyncInterval()
	want := 36 * time.Hour
	if got != want {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestComputeFullSyncIntervalWithoutSettingService(t *testing.T) {
	svc := &ProductMappingService{}
	got := svc.computeFullSyncInterval()
	if got != fullSyncIntervalFloor {
		t.Fatalf("expected floor when settingService=nil, got %v", got)
	}
}

func TestUpdateUpstreamSyncConfigClampsAboveMaximum(t *testing.T) {
	repo := newMockSettingRepo()
	svc := NewSettingService(repo)

	result, err := svc.Update(constants.SettingKeyUpstreamSyncConfig, map[string]interface{}{
		"interval_minutes": 99999,
	})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	assertSettingIntValue(t, result, "interval_minutes", upstreamSyncIntervalMinMax)
}
