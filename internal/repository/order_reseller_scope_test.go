package repository

import (
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func seedScopedOrder(t *testing.T, db *gorm.DB, orderNo string, userID uint, guestEmail string, guestPassword string, status string, resellerID *uint, parentID *uint) models.Order {
	t.Helper()
	order := models.Order{
		OrderNo:          orderNo,
		ParentID:         parentID,
		UserID:           userID,
		GuestEmail:       guestEmail,
		GuestPassword:    guestPassword,
		Status:           status,
		Currency:         "USD",
		OriginalAmount:   models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
		TotalAmount:      models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
		OnlinePaidAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
		ResellerID:       resellerID,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order %s failed: %v", orderNo, err)
	}
	return order
}

func assertOrderFound(t *testing.T, order *models.Order, wantOrderNo string) {
	t.Helper()
	if order == nil {
		t.Fatalf("expected order %s, got nil", wantOrderNo)
	}
	if order.OrderNo != wantOrderNo {
		t.Fatalf("expected order_no %s, got %s", wantOrderNo, order.OrderNo)
	}
}

func assertOrderMissing(t *testing.T, order *models.Order) {
	t.Helper()
	if order != nil {
		t.Fatalf("expected nil order, got %+v", order)
	}
}

func TestOrderRepositoryTenantScopePointQueriesAndLists(t *testing.T) {
	db := openResellerPricingRepoTestDB(t)
	repo := NewOrderRepository(db)
	user := models.User{Email: "scope-user@example.com", PasswordHash: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	resellerOne := uint(101)
	resellerTwo := uint(202)
	mainScope := ResellerOrderScope{}
	resellerOneScope := ResellerOrderScope{ResellerID: &resellerOne}
	resellerTwoScope := ResellerOrderScope{ResellerID: &resellerTwo}

	mainOrder := seedScopedOrder(t, db, "SCOPE-MAIN", user.ID, "", "", constants.OrderStatusPendingPayment, nil, nil)
	resellerOrder := seedScopedOrder(t, db, "SCOPE-R1", user.ID, "", "", constants.OrderStatusPaid, &resellerOne, nil)
	resellerTwoOrder := seedScopedOrder(t, db, "SCOPE-R2", user.ID, "", "", constants.OrderStatusPaid, &resellerTwo, nil)
	child := seedScopedOrder(t, db, "SCOPE-R1-CHILD", user.ID, "", "", constants.OrderStatusPaid, &resellerOne, &resellerOrder.ID)
	guestMain := seedScopedOrder(t, db, "GUEST-MAIN", 0, "guest@example.com", "code", constants.OrderStatusPendingPayment, nil, nil)
	guestReseller := seedScopedOrder(t, db, "GUEST-R1", 0, "guest@example.com", "code", constants.OrderStatusPaid, &resellerOne, nil)
	_ = resellerTwoOrder
	_ = child
	_ = guestMain
	_ = guestReseller

	got, err := repo.GetByOrderNoAndUserScoped(mainOrder.OrderNo, user.ID, mainScope)
	if err != nil {
		t.Fatalf("GetByOrderNoAndUserScoped main failed: %v", err)
	}
	assertOrderFound(t, got, mainOrder.OrderNo)
	got, err = repo.GetByOrderNoAndUserScoped(mainOrder.OrderNo, user.ID, resellerOneScope)
	if err != nil {
		t.Fatalf("GetByOrderNoAndUserScoped main from reseller failed: %v", err)
	}
	assertOrderMissing(t, got)

	got, err = repo.GetByIDAndUserScoped(resellerOrder.ID, user.ID, resellerOneScope)
	if err != nil {
		t.Fatalf("GetByIDAndUserScoped reseller failed: %v", err)
	}
	assertOrderFound(t, got, resellerOrder.OrderNo)
	got, err = repo.GetByIDAndUserScoped(resellerOrder.ID, user.ID, mainScope)
	if err != nil {
		t.Fatalf("GetByIDAndUserScoped reseller from main failed: %v", err)
	}
	assertOrderMissing(t, got)
	got, err = repo.GetByIDAndUserScoped(resellerOrder.ID, user.ID, resellerTwoScope)
	if err != nil {
		t.Fatalf("GetByIDAndUserScoped reseller from other reseller failed: %v", err)
	}
	assertOrderMissing(t, got)

	got, err = repo.GetAnyByOrderNoAndUserScoped("SCOPE-R1-CHILD", user.ID, resellerOneScope)
	if err != nil {
		t.Fatalf("GetAnyByOrderNoAndUserScoped child failed: %v", err)
	}
	assertOrderFound(t, got, "SCOPE-R1-CHILD")
	got, err = repo.GetAnyByOrderNoAndUserScoped("SCOPE-R1-CHILD", user.ID, mainScope)
	if err != nil {
		t.Fatalf("GetAnyByOrderNoAndUserScoped child from main failed: %v", err)
	}
	assertOrderMissing(t, got)

	guestGot, err := repo.GetByOrderNoAndGuestScoped("GUEST-MAIN", "guest@example.com", "code", mainScope)
	if err != nil {
		t.Fatalf("GetByOrderNoAndGuestScoped main failed: %v", err)
	}
	assertOrderFound(t, guestGot, "GUEST-MAIN")
	guestGot, err = repo.GetByOrderNoAndGuestScoped("GUEST-MAIN", "guest@example.com", "code", resellerOneScope)
	if err != nil {
		t.Fatalf("GetByOrderNoAndGuestScoped main from reseller failed: %v", err)
	}
	assertOrderMissing(t, guestGot)
	guestGot, err = repo.GetByIDAndGuestScoped(guestReseller.ID, "guest@example.com", "code", resellerOneScope)
	if err != nil {
		t.Fatalf("GetByIDAndGuestScoped reseller failed: %v", err)
	}
	assertOrderFound(t, guestGot, "GUEST-R1")
	guestGot, err = repo.GetByIDAndGuestScoped(guestReseller.ID, "guest@example.com", "code", resellerTwoScope)
	if err != nil {
		t.Fatalf("GetByIDAndGuestScoped reseller from other reseller failed: %v", err)
	}
	assertOrderMissing(t, guestGot)

	mainRows, mainTotal, err := repo.ListByUserScoped(OrderListFilter{UserID: user.ID, Page: 1, PageSize: 20}, mainScope)
	if err != nil {
		t.Fatalf("ListByUserScoped main failed: %v", err)
	}
	if mainTotal != 1 || len(mainRows) != 1 || mainRows[0].OrderNo != "SCOPE-MAIN" {
		t.Fatalf("main list mismatch total=%d rows=%+v", mainTotal, mainRows)
	}
	resellerRows, resellerTotal, err := repo.ListByUserScoped(OrderListFilter{UserID: user.ID, Page: 1, PageSize: 20}, resellerOneScope)
	if err != nil {
		t.Fatalf("ListByUserScoped reseller failed: %v", err)
	}
	if resellerTotal != 1 || len(resellerRows) != 1 || resellerRows[0].OrderNo != "SCOPE-R1" {
		t.Fatalf("reseller list mismatch total=%d rows=%+v", resellerTotal, resellerRows)
	}

	mainStats, err := repo.StatsByUserScoped(OrderListFilter{UserID: user.ID}, mainScope)
	if err != nil {
		t.Fatalf("StatsByUserScoped main failed: %v", err)
	}
	if mainStats[constants.OrderStatusPendingPayment] != 1 || len(mainStats) != 1 {
		t.Fatalf("main stats mismatch: %+v", mainStats)
	}
	resellerStats, err := repo.StatsByUserScoped(OrderListFilter{UserID: user.ID}, resellerOneScope)
	if err != nil {
		t.Fatalf("StatsByUserScoped reseller failed: %v", err)
	}
	if resellerStats[constants.OrderStatusPaid] != 1 || len(resellerStats) != 1 {
		t.Fatalf("reseller stats mismatch: %+v", resellerStats)
	}

	guestRows, guestTotal, err := repo.ListByGuestScoped("guest@example.com", "code", 1, 20, resellerOneScope)
	if err != nil {
		t.Fatalf("ListByGuestScoped reseller failed: %v", err)
	}
	if guestTotal != 1 || len(guestRows) != 1 || guestRows[0].OrderNo != "GUEST-R1" {
		t.Fatalf("guest reseller list mismatch total=%d rows=%+v", guestTotal, guestRows)
	}
}
