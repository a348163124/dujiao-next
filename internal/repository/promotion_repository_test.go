package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupPromotionRepositoryTest(t *testing.T) *GormPromotionRepository {
	t.Helper()
	dsn := fmt.Sprintf("file:promotion_repository_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&models.Promotion{}); err != nil {
		t.Fatalf("migrate promotion failed: %v", err)
	}
	return NewPromotionRepository(db)
}

func createTestPromotion(t *testing.T, repo *GormPromotionRepository, name string) {
	t.Helper()
	promo := &models.Promotion{
		Name:       name,
		ScopeType:  constants.ScopeTypeProduct,
		ScopeRefID: 1,
		Type:       constants.PromotionTypePercent,
		Value:      models.NewMoneyFromDecimal(decimal.NewFromInt(90)),
		IsActive:   true,
	}
	if err := repo.Create(promo); err != nil {
		t.Fatalf("create promotion %q failed: %v", name, err)
	}
}

// 按活动名称模糊检索：只返回名称包含关键字的活动
func TestPromotionRepositoryListFilterByName(t *testing.T) {
	repo := setupPromotionRepositoryTest(t)
	createTestPromotion(t, repo, "双十一大促")
	createTestPromotion(t, repo, "双十二活动")
	createTestPromotion(t, repo, "New Year Sale")

	rows, total, err := repo.List(PromotionListFilter{Name: "双十一"})
	if err != nil {
		t.Fatalf("list promotions failed: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("expected 1 match for 双十一, got total=%d rows=%d", total, len(rows))
	}
	if rows[0].Name != "双十一大促" {
		t.Fatalf("expected 双十一大促, got %s", rows[0].Name)
	}
}

// 名称检索需大小写不敏感，保证 SQLite 与 PostgreSQL 行为一致
func TestPromotionRepositoryListFilterByNameCaseInsensitive(t *testing.T) {
	repo := setupPromotionRepositoryTest(t)
	createTestPromotion(t, repo, "New Year Sale")
	createTestPromotion(t, repo, "Summer Clearance")

	rows, total, err := repo.List(PromotionListFilter{Name: "year sale"})
	if err != nil {
		t.Fatalf("list promotions failed: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("expected 1 case-insensitive match, got total=%d rows=%d", total, len(rows))
	}
	if rows[0].Name != "New Year Sale" {
		t.Fatalf("expected New Year Sale, got %s", rows[0].Name)
	}
}

// 空名称过滤不应影响结果（保持向后兼容）
func TestPromotionRepositoryListEmptyNameReturnsAll(t *testing.T) {
	repo := setupPromotionRepositoryTest(t)
	createTestPromotion(t, repo, "活动A")
	createTestPromotion(t, repo, "活动B")

	rows, total, err := repo.List(PromotionListFilter{Name: ""})
	if err != nil {
		t.Fatalf("list promotions failed: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("expected all 2 promotions with empty name filter, got total=%d rows=%d", total, len(rows))
	}
}
