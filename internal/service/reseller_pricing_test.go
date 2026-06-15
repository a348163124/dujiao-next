package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type resellerPricingRepoStub struct {
	profile          *models.ResellerProfile
	settings         []models.ResellerProductSetting
	related          map[uint]bool
	profileQueries   int
	settingsQueries  int
	relatedQueries   int
	hiddenProductIDs []uint
	snapshots        []*models.ResellerOrderSnapshot
}

func (r *resellerPricingRepoStub) Transaction(fn func(tx *gorm.DB) error) error {
	return fn(nil)
}

func (r *resellerPricingRepoStub) WithTx(tx *gorm.DB) repository.ResellerRepository {
	return r
}

func (r *resellerPricingRepoStub) CreateProfile(profile *models.ResellerProfile) error {
	r.profile = profile
	return nil
}

func (r *resellerPricingRepoStub) GetProfileByID(id uint) (*models.ResellerProfile, error) {
	r.profileQueries++
	if r.profile == nil || r.profile.ID != id {
		return nil, nil
	}
	profile := *r.profile
	return &profile, nil
}

func (r *resellerPricingRepoStub) GetProfileByUserID(userID uint) (*models.ResellerProfile, error) {
	if r.profile == nil || r.profile.UserID != userID {
		return nil, nil
	}
	profile := *r.profile
	return &profile, nil
}

func (r *resellerPricingRepoStub) UpsertDomain(domain models.ResellerDomain) (*models.ResellerDomain, error) {
	return nil, errors.New("not implemented")
}

func (r *resellerPricingRepoStub) FindDomainByHost(host string) (*models.ResellerDomain, error) {
	return nil, errors.New("not implemented")
}

func (r *resellerPricingRepoStub) FindActiveVerifiedDomain(host string) (*models.ResellerDomain, error) {
	return nil, errors.New("not implemented")
}

func (r *resellerPricingRepoStub) UpsertSiteConfig(config models.ResellerSiteConfig) (*models.ResellerSiteConfig, error) {
	return nil, errors.New("not implemented")
}

func (r *resellerPricingRepoStub) ListProductSettingsForPricing(resellerID uint, productIDs []uint, skuIDs []uint) ([]models.ResellerProductSetting, error) {
	r.settingsQueries++
	rows := make([]models.ResellerProductSetting, len(r.settings))
	copy(rows, r.settings)
	return rows, nil
}

func (r *resellerPricingRepoStub) ListHiddenProductIDs(resellerID uint) ([]uint, error) {
	return append([]uint(nil), r.hiddenProductIDs...), nil
}

func (r *resellerPricingRepoStub) IsActiveRelatedAccount(resellerID uint, userID uint) (bool, error) {
	r.relatedQueries++
	return r.related[userID], nil
}

func (r *resellerPricingRepoStub) CreateOrderSnapshot(snapshot *models.ResellerOrderSnapshot) error {
	r.snapshots = append(r.snapshots, snapshot)
	return nil
}

func (r *resellerPricingRepoStub) GetOrderSnapshotByOrderID(orderID uint) (*models.ResellerOrderSnapshot, error) {
	for _, snapshot := range r.snapshots {
		if snapshot.OrderID == orderID {
			return snapshot, nil
		}
	}
	return nil, nil
}

func testResellerProfile() *models.ResellerProfile {
	return &models.ResellerProfile{
		ID:                   10,
		UserID:               99,
		Status:               models.ResellerProfileStatusActive,
		DefaultMarkupPercent: models.NewMoneyFromDecimal(decimal.NewFromInt(20)),
		MaxMarkupPercent:     models.NewMoneyFromDecimal(decimal.NewFromInt(50)),
	}
}

func testResellerTenant() TenantContext {
	return ResellerTenantContext("alias.example.test", 10, 88, "primary.example.test")
}

func testOrderBuildResult(items ...struct {
	productID uint
	skuID     uint
	base      decimal.Decimal
	cost      decimal.Decimal
	quantity  int
}) *orderBuildResult {
	plans := make([]childOrderPlan, 0, len(items))
	for _, item := range items {
		product := &models.Product{ID: item.productID, TitleJSON: models.JSON{"zh-CN": fmt.Sprintf("p%d", item.productID)}}
		sku := &models.ProductSKU{
			ID:              item.skuID,
			ProductID:       item.productID,
			PriceAmount:     models.NewMoneyFromDecimal(item.base),
			CostPriceAmount: models.NewMoneyFromDecimal(item.cost),
			IsActive:        true,
		}
		baseTotal := item.base.Mul(decimal.NewFromInt(int64(item.quantity))).Round(2)
		orderItem := models.OrderItem{
			ProductID:          item.productID,
			SKUID:              item.skuID,
			TitleJSON:          product.TitleJSON,
			SKUSnapshotJSON:    models.JSON{"sku_id": item.skuID},
			OriginalUnitPrice:  models.NewMoneyFromDecimal(item.base),
			UnitPrice:          models.NewMoneyFromDecimal(item.base),
			CostPrice:          models.NewMoneyFromDecimal(item.cost),
			Quantity:           item.quantity,
			OriginalTotalPrice: models.NewMoneyFromDecimal(baseTotal),
			TotalPrice:         models.NewMoneyFromDecimal(baseTotal),
			FulfillmentType:    constants.FulfillmentTypeManual,
		}
		plans = append(plans, childOrderPlan{
			Product:     product,
			SKU:         sku,
			Item:        orderItem,
			TotalAmount: baseTotal,
			Currency:    "USD",
		})
	}
	total := decimal.Zero
	for _, plan := range plans {
		total = total.Add(plan.TotalAmount).Round(2)
	}
	return &orderBuildResult{
		Plans:          plans,
		OrderItems:     []models.OrderItem{},
		OriginalAmount: total,
		TotalAmount:    total,
		Currency:       "USD",
	}
}

func TestResellerPricingResolverMainTenantNoop(t *testing.T) {
	repo := &resellerPricingRepoStub{profile: testResellerProfile()}
	resolver := NewResellerPricingResolver(repo)
	result := testOrderBuildResult(struct {
		productID uint
		skuID     uint
		base      decimal.Decimal
		cost      decimal.Decimal
		quantity  int
	}{productID: 1, skuID: 11, base: decimal.NewFromInt(100), cost: decimal.NewFromInt(50), quantity: 1})

	ctx, err := resolver.ApplyToOrderBuildResult(MainTenantContext("main.example.test"), 123, result)
	if err != nil {
		t.Fatalf("ApplyToOrderBuildResult main failed: %v", err)
	}
	if ctx != nil {
		t.Fatalf("main tenant should not produce pricing context: %+v", ctx)
	}
	if repo.profileQueries != 0 || repo.settingsQueries != 0 || repo.relatedQueries != 0 {
		t.Fatalf("main tenant should not query reseller repo, got profile=%d settings=%d related=%d", repo.profileQueries, repo.settingsQueries, repo.relatedQueries)
	}
	if !result.TotalAmount.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("main total changed: %s", result.TotalAmount)
	}
}

func TestResellerPricingResolverAppliesPriorityAndDefaultMarkup(t *testing.T) {
	repo := &resellerPricingRepoStub{
		profile: testResellerProfile(),
		settings: []models.ResellerProductSetting{
			{
				ID:               1,
				ResellerID:       10,
				ProductID:        1,
				SKUID:            0,
				IsListed:         true,
				PricingMode:      models.ResellerPricingModeMarkupPercent,
				MarkupPercent:    models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
				FixedPriceAmount: models.NewMoneyFromDecimal(decimal.Zero),
			},
			{
				ID:               2,
				ResellerID:       10,
				ProductID:        1,
				SKUID:            11,
				IsListed:         true,
				PricingMode:      models.ResellerPricingModeFixedPrice,
				FixedPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
			},
			{
				ID:                3,
				ResellerID:        10,
				ProductID:         2,
				SKUID:             22,
				IsListed:          true,
				PricingMode:       models.ResellerPricingModeFixedMarkup,
				FixedMarkupAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(25)),
			},
		},
		related: map[uint]bool{},
	}
	resolver := NewResellerPricingResolver(repo)
	result := testOrderBuildResult(
		struct {
			productID uint
			skuID     uint
			base      decimal.Decimal
			cost      decimal.Decimal
			quantity  int
		}{productID: 1, skuID: 11, base: decimal.NewFromInt(100), cost: decimal.NewFromInt(50), quantity: 1},
		struct {
			productID uint
			skuID     uint
			base      decimal.Decimal
			cost      decimal.Decimal
			quantity  int
		}{productID: 2, skuID: 22, base: decimal.NewFromInt(80), cost: decimal.NewFromInt(40), quantity: 2},
		struct {
			productID uint
			skuID     uint
			base      decimal.Decimal
			cost      decimal.Decimal
			quantity  int
		}{productID: 3, skuID: 33, base: decimal.NewFromInt(50), cost: decimal.NewFromInt(25), quantity: 1},
	)

	ctx, err := resolver.ApplyToOrderBuildResult(testResellerTenant(), 123, result)
	if err != nil {
		t.Fatalf("ApplyToOrderBuildResult reseller failed: %v", err)
	}
	if ctx == nil {
		t.Fatal("expected reseller pricing context")
	}
	if repo.settingsQueries != 1 {
		t.Fatalf("expected one settings query, got %d", repo.settingsQueries)
	}
	if ctx.ResellerUserID != 99 {
		t.Fatalf("snapshot should use fresh profile user id, got %d", ctx.ResellerUserID)
	}
	if ctx.Domain != "primary.example.test" {
		t.Fatalf("expected primary domain snapshot, got %q", ctx.Domain)
	}
	if !result.Plans[0].TotalAmount.Equal(decimal.NewFromInt(130)) {
		t.Fatalf("fixed price plan total mismatch: %s", result.Plans[0].TotalAmount)
	}
	if !result.Plans[1].TotalAmount.Equal(decimal.NewFromInt(210)) {
		t.Fatalf("fixed markup plan total mismatch: %s", result.Plans[1].TotalAmount)
	}
	if !result.Plans[2].TotalAmount.Equal(decimal.NewFromInt(60)) {
		t.Fatalf("profile default markup plan total mismatch: %s", result.Plans[2].TotalAmount)
	}
	if !result.TotalAmount.Equal(decimal.NewFromInt(400)) || !result.OriginalAmount.Equal(decimal.NewFromInt(400)) {
		t.Fatalf("parent totals must be derived from rewritten plan totals, got original=%s total=%s", result.OriginalAmount, result.TotalAmount)
	}
	if !ctx.BaseAmount.Equal(decimal.NewFromInt(310)) || !ctx.ResellerAmount.Equal(decimal.NewFromInt(400)) || !ctx.ProfitAmount.Equal(decimal.NewFromInt(90)) {
		t.Fatalf("snapshot totals mismatch base=%s reseller=%s profit=%s", ctx.BaseAmount, ctx.ResellerAmount, ctx.ProfitAmount)
	}
	if !ctx.EffectiveProfit.Equal(decimal.NewFromInt(90)) || !ctx.ProfitEligible {
		t.Fatalf("expected eligible effective profit 90, got eligible=%t effective=%s", ctx.ProfitEligible, ctx.EffectiveProfit)
	}
}

func TestResellerPricingResolverBlocksHiddenProductAndSKU(t *testing.T) {
	tests := []struct {
		name    string
		setting models.ResellerProductSetting
	}{
		{
			name: "product",
			setting: models.ResellerProductSetting{
				ID:          1,
				ResellerID:  10,
				ProductID:   1,
				SKUID:       0,
				IsListed:    false,
				PricingMode: models.ResellerPricingModeInherit,
			},
		},
		{
			name: "sku",
			setting: models.ResellerProductSetting{
				ID:          2,
				ResellerID:  10,
				ProductID:   1,
				SKUID:       11,
				IsListed:    false,
				PricingMode: models.ResellerPricingModeInherit,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &resellerPricingRepoStub{profile: testResellerProfile(), settings: []models.ResellerProductSetting{tt.setting}}
			resolver := NewResellerPricingResolver(repo)
			result := testOrderBuildResult(struct {
				productID uint
				skuID     uint
				base      decimal.Decimal
				cost      decimal.Decimal
				quantity  int
			}{productID: 1, skuID: 11, base: decimal.NewFromInt(100), cost: decimal.NewFromInt(50), quantity: 1})

			_, err := resolver.ApplyToOrderBuildResult(testResellerTenant(), 123, result)
			if !errors.Is(err, ErrResellerProductNotListed) {
				t.Fatalf("expected ErrResellerProductNotListed, got %v", err)
			}
		})
	}
}

func TestResellerPricingResolverValidatesPriceRules(t *testing.T) {
	tests := []struct {
		name    string
		profile *models.ResellerProfile
		setting models.ResellerProductSetting
		wantErr error
	}{
		{
			name:    "fixed price below base",
			profile: testResellerProfile(),
			setting: models.ResellerProductSetting{
				ID:               1,
				ResellerID:       10,
				ProductID:        1,
				SKUID:            11,
				IsListed:         true,
				PricingMode:      models.ResellerPricingModeFixedPrice,
				FixedPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(99)),
			},
			wantErr: ErrResellerPriceBelowBase,
		},
		{
			name:    "fixed markup below zero",
			profile: testResellerProfile(),
			setting: models.ResellerProductSetting{
				ID:                2,
				ResellerID:        10,
				ProductID:         1,
				SKUID:             11,
				IsListed:          true,
				PricingMode:       models.ResellerPricingModeFixedMarkup,
				FixedMarkupAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(-1)),
			},
			wantErr: ErrResellerPriceBelowBase,
		},
		{
			name:    "percent exceeds max",
			profile: testResellerProfile(),
			setting: models.ResellerProductSetting{
				ID:            3,
				ResellerID:    10,
				ProductID:     1,
				SKUID:         11,
				IsListed:      true,
				PricingMode:   models.ResellerPricingModeMarkupPercent,
				MarkupPercent: models.NewMoneyFromDecimal(decimal.NewFromInt(60)),
			},
			wantErr: ErrResellerMarkupExceeded,
		},
		{
			name:    "fixed price implicit percent exceeds max",
			profile: testResellerProfile(),
			setting: models.ResellerProductSetting{
				ID:               4,
				ResellerID:       10,
				ProductID:        1,
				SKUID:            11,
				IsListed:         true,
				PricingMode:      models.ResellerPricingModeFixedPrice,
				FixedPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(151)),
			},
			wantErr: ErrResellerMarkupExceeded,
		},
		{
			name:    "fixed markup implicit percent exceeds max",
			profile: testResellerProfile(),
			setting: models.ResellerProductSetting{
				ID:                5,
				ResellerID:        10,
				ProductID:         1,
				SKUID:             11,
				IsListed:          true,
				PricingMode:       models.ResellerPricingModeFixedMarkup,
				FixedMarkupAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(51)),
			},
			wantErr: ErrResellerMarkupExceeded,
		},
		{
			name:    "unknown mode",
			profile: testResellerProfile(),
			setting: models.ResellerProductSetting{
				ID:          6,
				ResellerID:  10,
				ProductID:   1,
				SKUID:       11,
				IsListed:    true,
				PricingMode: "surprise",
			},
			wantErr: ErrResellerPricingModeInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &resellerPricingRepoStub{profile: tt.profile, settings: []models.ResellerProductSetting{tt.setting}}
			resolver := NewResellerPricingResolver(repo)
			result := testOrderBuildResult(struct {
				productID uint
				skuID     uint
				base      decimal.Decimal
				cost      decimal.Decimal
				quantity  int
			}{productID: 1, skuID: 11, base: decimal.NewFromInt(100), cost: decimal.NewFromInt(50), quantity: 1})

			_, err := resolver.ApplyToOrderBuildResult(testResellerTenant(), 123, result)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestResellerPricingResolverSelfDealingRiskSnapshot(t *testing.T) {
	tests := []struct {
		name       string
		buyerID    uint
		related    map[uint]bool
		wantReason string
		wantElig   bool
	}{
		{name: "owner", buyerID: 99, related: map[uint]bool{}, wantReason: "self_dealing_owner", wantElig: false},
		{name: "related", buyerID: 123, related: map[uint]bool{123: true}, wantReason: "self_dealing_related_account", wantElig: false},
		{name: "guest", buyerID: 0, related: map[uint]bool{}, wantReason: "", wantElig: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &resellerPricingRepoStub{profile: testResellerProfile(), related: tt.related}
			resolver := NewResellerPricingResolver(repo)
			result := testOrderBuildResult(struct {
				productID uint
				skuID     uint
				base      decimal.Decimal
				cost      decimal.Decimal
				quantity  int
			}{productID: 1, skuID: 11, base: decimal.NewFromInt(100), cost: decimal.NewFromInt(50), quantity: 1})

			ctx, err := resolver.ApplyToOrderBuildResult(testResellerTenant(), tt.buyerID, result)
			if err != nil {
				t.Fatalf("ApplyToOrderBuildResult failed: %v", err)
			}
			if ctx.ProfitEligible != tt.wantElig || ctx.ProfitBlockReason != tt.wantReason {
				t.Fatalf("risk mismatch eligible=%t reason=%q", ctx.ProfitEligible, ctx.ProfitBlockReason)
			}
			if tt.wantElig && !ctx.EffectiveProfit.Equal(decimal.NewFromInt(20)) {
				t.Fatalf("expected effective profit 20, got %s", ctx.EffectiveProfit)
			}
			if !tt.wantElig && !ctx.EffectiveProfit.Equal(decimal.Zero) {
				t.Fatalf("blocked profit must be zero, got %s", ctx.EffectiveProfit)
			}
			if got := ctx.RiskSnapshot["buyer_user_id"]; got != tt.buyerID {
				t.Fatalf("risk snapshot buyer_user_id mismatch want %d got %#v", tt.buyerID, got)
			}
		})
	}
}

func TestResellerPricingResolverDisplayBatchUsesSingleSettingsLookup(t *testing.T) {
	repo := &resellerPricingRepoStub{
		profile: testResellerProfile(),
		settings: []models.ResellerProductSetting{
			{
				ID:               1,
				ResellerID:       10,
				ProductID:        1,
				SKUID:            11,
				IsListed:         true,
				PricingMode:      models.ResellerPricingModeFixedPrice,
				FixedPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
			},
			{
				ID:          2,
				ResellerID:  10,
				ProductID:   2,
				SKUID:       22,
				IsListed:    false,
				PricingMode: models.ResellerPricingModeInherit,
			},
		},
	}
	resolver := NewResellerPricingResolver(repo)
	products := []models.Product{
		{
			ID:          1,
			PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
			SKUs: []models.ProductSKU{
				{ID: 11, ProductID: 1, PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)), CostPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(50)), IsActive: true},
			},
		},
		{
			ID:          2,
			PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(80)),
			SKUs: []models.ProductSKU{
				{ID: 22, ProductID: 2, PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(80)), CostPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(40)), IsActive: true},
			},
		},
	}

	batch, err := resolver.LoadDisplayPricingBatch(testResellerTenant(), products)
	if err != nil {
		t.Fatalf("LoadDisplayPricingBatch failed: %v", err)
	}
	if repo.settingsQueries != 1 {
		t.Fatalf("expected one settings query for page, got %d", repo.settingsQueries)
	}
	first, err := resolver.ResolveDisplayPrices(testResellerTenant(), &products[0], batch)
	if err != nil {
		t.Fatalf("ResolveDisplayPrices first failed: %v", err)
	}
	second, err := resolver.ResolveDisplayPrices(testResellerTenant(), &products[1], batch)
	if err != nil {
		t.Fatalf("ResolveDisplayPrices second failed: %v", err)
	}
	if repo.settingsQueries != 1 {
		t.Fatalf("ResolveDisplayPrices must not query per product, got %d settings queries", repo.settingsQueries)
	}
	if !first.Visible || first.DisplaySKUID != 11 || !first.DisplayPrice.Decimal.Equal(decimal.NewFromInt(130)) {
		t.Fatalf("first display mismatch: %+v", first)
	}
	if second.Visible {
		t.Fatalf("all hidden sku product should not be visible: %+v", second)
	}
}
