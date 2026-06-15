package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/queue"
	"github.com/dujiao-next/internal/repository"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type fakeOrderTimeoutQueue struct {
	enqueued int
}

func (q *fakeOrderTimeoutQueue) Enabled() bool {
	return true
}

func (q *fakeOrderTimeoutQueue) EnqueueOrderTimeoutCancel(payload queue.OrderTimeoutCancelPayload, delay time.Duration) error {
	q.enqueued++
	return nil
}

func (q *fakeOrderTimeoutQueue) EnqueueOrderStatusEmail(payload queue.OrderStatusEmailPayload, opts ...asynq.Option) error {
	return nil
}

type orderResellerSnapshotFixture struct {
	db           *gorm.DB
	svc          *OrderService
	resellerRepo repository.ResellerRepository
	queue        *fakeOrderTimeoutQueue
	owner        models.User
	buyer        models.User
	profile      models.ResellerProfile
	product      models.Product
	sku          models.ProductSKU
	tenant       TenantContext
}

func newOrderResellerSnapshotFixture(t *testing.T) orderResellerSnapshotFixture {
	t.Helper()

	dsn := fmt.Sprintf("file:order_reseller_snapshot_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.User{},
		&models.Category{},
		&models.Product{},
		&models.ProductSKU{},
		&models.Order{},
		&models.OrderItem{},
		&models.Fulfillment{},
		&models.Coupon{},
		&models.CouponUsage{},
		&models.Promotion{},
		&models.Payment{},
		&models.ResellerProfile{},
		&models.ResellerProductSetting{},
		&models.ResellerRelatedAccount{},
		&models.ResellerOrderSnapshot{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	category := models.Category{Slug: "reseller-order", NameJSON: models.JSON{"zh-CN": "reseller-order"}, IsActive: true}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category failed: %v", err)
	}
	owner := models.User{Email: "reseller-order-owner@example.com", PasswordHash: "hash"}
	buyer := models.User{Email: "reseller-order-buyer@example.com", PasswordHash: "hash"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner failed: %v", err)
	}
	if err := db.Create(&buyer).Error; err != nil {
		t.Fatalf("create buyer failed: %v", err)
	}
	profile := models.ResellerProfile{
		UserID:               owner.ID,
		Status:               models.ResellerProfileStatusActive,
		DefaultMarkupPercent: models.NewMoneyFromDecimal(decimal.NewFromInt(20)),
		MaxMarkupPercent:     models.NewMoneyFromDecimal(decimal.NewFromInt(80)),
	}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile failed: %v", err)
	}
	product := models.Product{
		CategoryID:      category.ID,
		Slug:            "reseller-order-product",
		TitleJSON:       models.JSON{"zh-CN": "reseller-order-product"},
		PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		WholesalePrices: models.WholesalePriceTiers{{MinQuantity: 2, UnitPrice: models.NewMoneyFromDecimal(decimal.NewFromInt(80))}},
		PurchaseType:    constants.ProductPurchaseGuest,
		FulfillmentType: constants.FulfillmentTypeManual,
		IsActive:        true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product failed: %v", err)
	}
	sku := models.ProductSKU{
		ProductID:        product.ID,
		SKUCode:          models.DefaultSKUCode,
		PriceAmount:      models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		CostPriceAmount:  models.NewMoneyFromDecimal(decimal.NewFromInt(50)),
		ManualStockTotal: constants.ManualStockUnlimited,
		IsActive:         true,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := db.Create(&sku).Error; err != nil {
		t.Fatalf("create sku failed: %v", err)
	}
	setting := models.ResellerProductSetting{
		ResellerID:       profile.ID,
		ProductID:        product.ID,
		SKUID:            sku.ID,
		IsListed:         true,
		PricingMode:      models.ResellerPricingModeFixedPrice,
		FixedPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
	}
	if err := db.Create(&setting).Error; err != nil {
		t.Fatalf("create reseller setting failed: %v", err)
	}
	startsAt := time.Now().Add(-time.Hour)
	endsAt := time.Now().Add(time.Hour)
	promotion := models.Promotion{
		Name:       "main-promotion",
		ScopeType:  constants.ScopeTypeProduct,
		ScopeRefID: product.ID,
		Type:       constants.PromotionTypeFixed,
		Value:      models.NewMoneyFromDecimal(decimal.NewFromInt(40)),
		MinAmount:  models.NewMoneyFromDecimal(decimal.Zero),
		IsActive:   true,
		StartsAt:   &startsAt,
		EndsAt:     &endsAt,
	}
	if err := db.Create(&promotion).Error; err != nil {
		t.Fatalf("create promotion failed: %v", err)
	}

	resellerRepo := repository.NewResellerRepository(db)
	orderRepo := repository.NewOrderRepository(db)
	q := &fakeOrderTimeoutQueue{}
	svc := NewOrderService(OrderServiceOptions{
		OrderRepo:               orderRepo,
		PaymentRepo:             repository.NewPaymentRepository(db),
		UserRepo:                repository.NewUserRepository(db),
		ProductRepo:             repository.NewProductRepository(db),
		ProductSKURepo:          repository.NewProductSKURepository(db),
		CardSecretRepo:          repository.NewCardSecretRepository(db),
		CouponRepo:              repository.NewCouponRepository(db),
		CouponUsageRepo:         repository.NewCouponUsageRepository(db),
		PromotionRepo:           repository.NewPromotionRepository(db),
		ExpireMinutes:           15,
		ResellerRepo:            resellerRepo,
		ResellerPricingResolver: NewResellerPricingResolver(resellerRepo),
	})
	svc.queueClient = q
	tenant := ResellerTenantContext("alias.example.test", profile.ID, owner.ID, "primary.example.test")
	return orderResellerSnapshotFixture{
		db:           db,
		svc:          svc,
		resellerRepo: resellerRepo,
		queue:        q,
		owner:        owner,
		buyer:        buyer,
		profile:      profile,
		product:      product,
		sku:          sku,
		tenant:       tenant,
	}
}

func (f orderResellerSnapshotFixture) createInput(userID uint) CreateOrderInput {
	return CreateOrderInput{
		UserID: userID,
		Tenant: f.tenant,
		Items:  []CreateOrderItem{{ProductID: f.product.ID, SKUID: f.sku.ID, Quantity: 1}},
	}
}

func TestPreviewOrderResellerUsesResellerFacingTotals(t *testing.T) {
	f := newOrderResellerSnapshotFixture(t)
	preview, err := f.svc.PreviewOrder(f.createInput(f.buyer.ID))
	if err != nil {
		t.Fatalf("PreviewOrder failed: %v", err)
	}
	if !preview.OriginalAmount.Decimal.Equal(decimal.NewFromInt(130)) || !preview.TotalAmount.Decimal.Equal(decimal.NewFromInt(130)) {
		t.Fatalf("expected reseller-facing total 130, got original=%s total=%s", preview.OriginalAmount.String(), preview.TotalAmount.String())
	}
	if !preview.PromotionDiscountAmount.Decimal.Equal(decimal.Zero) || !preview.WholesaleDiscountAmount.Decimal.Equal(decimal.Zero) || !preview.DiscountAmount.Decimal.Equal(decimal.Zero) {
		t.Fatalf("reseller preview should not expose main discounts: %+v", preview)
	}
	if len(preview.Items) != 1 || !preview.Items[0].UnitPrice.Decimal.Equal(decimal.NewFromInt(130)) || !preview.Items[0].OriginalUnitPrice.Decimal.Equal(decimal.NewFromInt(130)) {
		t.Fatalf("expected reseller-facing item prices, got %+v", preview.Items)
	}
	body, err := json.Marshal(preview)
	if err != nil {
		t.Fatalf("marshal preview failed: %v", err)
	}
	for _, forbidden := range []string{"reseller_id", "reseller_profit_amount", "base_amount", "pricing_snapshot_json"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("preview response leaked forbidden key %q: %s", forbidden, string(body))
		}
	}
}

func TestPreviewOrderResellerRejectsCouponCode(t *testing.T) {
	f := newOrderResellerSnapshotFixture(t)
	input := f.createInput(f.buyer.ID)
	input.CouponCode = "MAINCODE"
	_, err := f.svc.PreviewOrder(input)
	if !errors.Is(err, ErrResellerCouponNotAllowed) {
		t.Fatalf("expected ErrResellerCouponNotAllowed, got %v", err)
	}
}

func TestCreateOrderResellerWritesSnapshotAndTenantFields(t *testing.T) {
	f := newOrderResellerSnapshotFixture(t)
	order, err := f.svc.CreateOrder(f.createInput(f.buyer.ID))
	if err != nil {
		t.Fatalf("CreateOrder failed: %v", err)
	}
	if f.queue.enqueued != 1 {
		t.Fatalf("expected timeout cancel task enqueued once, got %d", f.queue.enqueued)
	}
	if order.ResellerID == nil || *order.ResellerID != f.profile.ID {
		t.Fatalf("parent reseller id mismatch: %+v", order.ResellerID)
	}
	if order.ResellerDomain != "primary.example.test" {
		t.Fatalf("parent reseller domain mismatch: %q", order.ResellerDomain)
	}
	if !order.TotalAmount.Decimal.Equal(decimal.NewFromInt(130)) || !order.ResellerProfitAmount.Decimal.Equal(decimal.NewFromInt(30)) {
		t.Fatalf("parent amounts mismatch total=%s profit=%s", order.TotalAmount.String(), order.ResellerProfitAmount.String())
	}
	if order.AffiliateProfileID != nil || order.AffiliateCode != "" {
		t.Fatalf("reseller order must not carry affiliate snapshot: profile=%v code=%q", order.AffiliateProfileID, order.AffiliateCode)
	}
	if len(order.Children) != 1 {
		t.Fatalf("expected one child order, got %d", len(order.Children))
	}
	child := order.Children[0]
	if child.ResellerID == nil || *child.ResellerID != f.profile.ID || child.ResellerDomain != "primary.example.test" {
		t.Fatalf("child reseller fields mismatch: %+v domain=%q", child.ResellerID, child.ResellerDomain)
	}
	if !child.TotalAmount.Decimal.Equal(decimal.NewFromInt(130)) || !child.ResellerProfitAmount.Decimal.Equal(decimal.NewFromInt(30)) {
		t.Fatalf("child amounts mismatch total=%s profit=%s", child.TotalAmount.String(), child.ResellerProfitAmount.String())
	}
	if len(child.Items) != 1 {
		t.Fatalf("expected child item, got %d", len(child.Items))
	}
	if !child.Items[0].UnitPrice.Decimal.Equal(decimal.NewFromInt(130)) || !child.Items[0].OriginalUnitPrice.Decimal.Equal(decimal.NewFromInt(130)) {
		t.Fatalf("child item should expose reseller-facing prices: %+v", child.Items[0])
	}

	snapshot, err := f.resellerRepo.GetOrderSnapshotByOrderID(order.ID)
	if err != nil {
		t.Fatalf("GetOrderSnapshotByOrderID failed: %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected reseller order snapshot")
	}
	if snapshot.Currency != order.Currency || snapshot.ResellerUserID != f.owner.ID || snapshot.BuyerUserID != f.buyer.ID {
		t.Fatalf("snapshot identity mismatch: %+v", snapshot)
	}
	if !snapshot.BaseAmount.Decimal.Equal(decimal.NewFromInt(100)) || !snapshot.ResellerAmount.Decimal.Equal(decimal.NewFromInt(130)) || !snapshot.ProfitAmount.Decimal.Equal(decimal.NewFromInt(30)) {
		t.Fatalf("snapshot amounts mismatch base=%s reseller=%s profit=%s", snapshot.BaseAmount.String(), snapshot.ResellerAmount.String(), snapshot.ProfitAmount.String())
	}
	if !snapshot.ProfitEligible || snapshot.ProfitBlockReason != "" {
		t.Fatalf("expected eligible snapshot, got eligible=%t reason=%q", snapshot.ProfitEligible, snapshot.ProfitBlockReason)
	}
	items, _ := snapshot.PricingSnapshotJSON["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("expected one pricing snapshot item, got %#v", snapshot.PricingSnapshotJSON["items"])
	}
	item, _ := items[0].(map[string]interface{})
	if item["child_order_id"] == nil || item["order_item_id"] == nil {
		t.Fatalf("snapshot item should include generated child/item ids: %+v", item)
	}
}

func TestCreateOrderResellerOwnerSelfDealingWritesZeroEffectiveProfit(t *testing.T) {
	f := newOrderResellerSnapshotFixture(t)
	order, err := f.svc.CreateOrder(f.createInput(f.owner.ID))
	if err != nil {
		t.Fatalf("CreateOrder owner failed: %v", err)
	}
	if !order.ResellerProfitAmount.Decimal.Equal(decimal.Zero) {
		t.Fatalf("self-dealing shortcut profit must be zero, got %s", order.ResellerProfitAmount.String())
	}
	snapshot, err := f.resellerRepo.GetOrderSnapshotByOrderID(order.ID)
	if err != nil {
		t.Fatalf("GetOrderSnapshotByOrderID failed: %v", err)
	}
	if snapshot == nil || snapshot.ProfitEligible || snapshot.ProfitBlockReason != "self_dealing_owner" {
		t.Fatalf("expected owner self-dealing snapshot, got %+v", snapshot)
	}
	if !snapshot.ProfitAmount.Decimal.Equal(decimal.NewFromInt(30)) {
		t.Fatalf("snapshot should keep calculated blocked profit, got %s", snapshot.ProfitAmount.String())
	}
}

func TestCreateGuestOrderResellerSnapshotBuyerUserIDZero(t *testing.T) {
	f := newOrderResellerSnapshotFixture(t)
	order, err := f.svc.CreateGuestOrder(CreateGuestOrderInput{
		Email:         "guest-reseller@example.com",
		OrderPassword: "guest-pass",
		Tenant:        f.tenant,
		Items:         []CreateOrderItem{{ProductID: f.product.ID, SKUID: f.sku.ID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("CreateGuestOrder failed: %v", err)
	}
	snapshot, err := f.resellerRepo.GetOrderSnapshotByOrderID(order.ID)
	if err != nil {
		t.Fatalf("GetOrderSnapshotByOrderID failed: %v", err)
	}
	if snapshot == nil || snapshot.BuyerUserID != 0 || !snapshot.ProfitEligible {
		t.Fatalf("guest snapshot mismatch: %+v", snapshot)
	}
	if got := snapshot.RiskSnapshotJSON["buyer_user_id"]; fmt.Sprint(got) != "0" {
		t.Fatalf("risk snapshot should record buyer_user_id=0, got %#v", got)
	}
}

func TestOrderServiceTenantScopedUserQueries(t *testing.T) {
	f := newOrderResellerSnapshotFixture(t)
	now := time.Now()
	mainOrder := models.Order{
		OrderNo:          "MAIN-SCOPED-USER",
		UserID:           f.buyer.ID,
		Status:           constants.OrderStatusPendingPayment,
		Currency:         constants.SiteCurrencyDefault,
		OriginalAmount:   models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		TotalAmount:      models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		OnlinePaidAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	resellerID := f.profile.ID
	resellerOrder := models.Order{
		OrderNo:          "RESELLER-SCOPED-USER",
		UserID:           f.buyer.ID,
		Status:           constants.OrderStatusPendingPayment,
		Currency:         constants.SiteCurrencyDefault,
		OriginalAmount:   models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		TotalAmount:      models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		OnlinePaidAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		ResellerID:       &resellerID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := f.db.Create(&mainOrder).Error; err != nil {
		t.Fatalf("create main order failed: %v", err)
	}
	if err := f.db.Create(&resellerOrder).Error; err != nil {
		t.Fatalf("create reseller order failed: %v", err)
	}

	if _, err := f.svc.GetOrderByUserOrderNoForTenant(MainTenantContext("main.example.test"), resellerOrder.OrderNo, f.buyer.ID); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("main scope should not read reseller order, got %v", err)
	}
	got, err := f.svc.GetOrderByUserOrderNoForTenant(f.tenant, resellerOrder.OrderNo, f.buyer.ID)
	if err != nil {
		t.Fatalf("reseller scope should read reseller order: %v", err)
	}
	if got.ID != resellerOrder.ID {
		t.Fatalf("reseller scoped order mismatch want %d got %d", resellerOrder.ID, got.ID)
	}
	orders, total, err := f.svc.ListOrdersByUserForTenant(f.tenant, repository.OrderListFilter{UserID: f.buyer.ID, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListOrdersByUserForTenant failed: %v", err)
	}
	if total != 1 || len(orders) != 1 || orders[0].ID != resellerOrder.ID {
		t.Fatalf("expected only reseller order, total=%d orders=%+v", total, orders)
	}
	stats, err := f.svc.StatsOrdersByUserForTenant(MainTenantContext("main.example.test"), repository.OrderListFilter{UserID: f.buyer.ID})
	if err != nil {
		t.Fatalf("StatsOrdersByUserForTenant failed: %v", err)
	}
	if stats[constants.OrderStatusPendingPayment] != 1 {
		t.Fatalf("main stats should count only main order, got %+v", stats)
	}
}

func TestOrderServiceTenantScopedGuestQueries(t *testing.T) {
	f := newOrderResellerSnapshotFixture(t)
	now := time.Now()
	resellerID := f.profile.ID
	mainOrder := models.Order{
		OrderNo:          "MAIN-SCOPED-GUEST",
		GuestEmail:       "scoped-guest@example.com",
		GuestPassword:    "pw",
		Status:           constants.OrderStatusPendingPayment,
		Currency:         constants.SiteCurrencyDefault,
		OriginalAmount:   models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		TotalAmount:      models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		OnlinePaidAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	resellerOrder := models.Order{
		OrderNo:          "RESELLER-SCOPED-GUEST",
		GuestEmail:       "scoped-guest@example.com",
		GuestPassword:    "pw",
		Status:           constants.OrderStatusPendingPayment,
		Currency:         constants.SiteCurrencyDefault,
		OriginalAmount:   models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		TotalAmount:      models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		OnlinePaidAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		ResellerID:       &resellerID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := f.db.Create(&mainOrder).Error; err != nil {
		t.Fatalf("create main guest order failed: %v", err)
	}
	if err := f.db.Create(&resellerOrder).Error; err != nil {
		t.Fatalf("create reseller guest order failed: %v", err)
	}

	if _, err := f.svc.GetOrderByGuestOrderNoForTenant(MainTenantContext("main.example.test"), resellerOrder.OrderNo, "scoped-guest@example.com", "pw"); !errors.Is(err, ErrGuestOrderNotFound) {
		t.Fatalf("main scope should not read reseller guest order, got %v", err)
	}
	got, err := f.svc.GetOrderByGuestOrderNoForTenant(f.tenant, resellerOrder.OrderNo, "scoped-guest@example.com", "pw")
	if err != nil {
		t.Fatalf("reseller scope should read reseller guest order: %v", err)
	}
	if got.ID != resellerOrder.ID {
		t.Fatalf("reseller scoped guest order mismatch want %d got %d", resellerOrder.ID, got.ID)
	}
	orders, total, err := f.svc.ListOrdersByGuestForTenant(f.tenant, "scoped-guest@example.com", "pw", 1, 20)
	if err != nil {
		t.Fatalf("ListOrdersByGuestForTenant failed: %v", err)
	}
	if total != 1 || len(orders) != 1 || orders[0].ID != resellerOrder.ID {
		t.Fatalf("expected only reseller guest order, total=%d orders=%+v", total, orders)
	}
}
