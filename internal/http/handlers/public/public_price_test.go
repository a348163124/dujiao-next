package public

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/provider"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestDecoratePublicProductDisplayPricePrefersFirstActiveSKU(t *testing.T) {
	h := &Handler{}
	product := &models.Product{
		ID:          1,
		PriceAmount: models.NewMoneyFromDecimal(decimal.RequireFromString("59.90")),
		SKUs: []models.ProductSKU{
			{
				ID:          11,
				IsActive:    true,
				SortOrder:   100,
				PriceAmount: models.NewMoneyFromDecimal(decimal.RequireFromString("89.90")),
			},
			{
				ID:          12,
				IsActive:    true,
				SortOrder:   10,
				PriceAmount: models.NewMoneyFromDecimal(decimal.RequireFromString("49.90")),
			},
		},
	}

	item, err := h.decoratePublicProduct(product, nil)
	if err != nil {
		t.Fatalf("decoratePublicProduct failed: %v", err)
	}
	expected := decimal.RequireFromString("89.90")
	if !item.PriceAmount.Decimal.Equal(expected) {
		t.Fatalf("expected display price %s, got: %s", expected.String(), item.PriceAmount.String())
	}
}

func TestDecoratePublicProductPromotionUsesDisplayPrice(t *testing.T) {
	dsn := fmt.Sprintf("file:public_product_display_price_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&models.Promotion{}); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	promotion := models.Promotion{
		Name:       "fixed-10",
		ScopeType:  constants.ScopeTypeProduct,
		ScopeRefID: 1,
		Type:       constants.PromotionTypeFixed,
		Value:      models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
		MinAmount:  models.NewMoneyFromDecimal(decimal.Zero),
		IsActive:   true,
	}
	if err := db.Create(&promotion).Error; err != nil {
		t.Fatalf("create promotion failed: %v", err)
	}

	h := &Handler{}
	product := &models.Product{
		ID:          1,
		PriceAmount: models.NewMoneyFromDecimal(decimal.RequireFromString("59.90")),
		SKUs: []models.ProductSKU{
			{
				ID:          21,
				IsActive:    true,
				SortOrder:   100,
				PriceAmount: models.NewMoneyFromDecimal(decimal.RequireFromString("89.90")),
			},
			{
				ID:          22,
				IsActive:    true,
				SortOrder:   10,
				PriceAmount: models.NewMoneyFromDecimal(decimal.RequireFromString("49.90")),
			},
		},
	}

	promoService := service.NewPromotionService(repository.NewPromotionRepository(db))
	item, err := h.decoratePublicProduct(product, promoService)
	if err != nil {
		t.Fatalf("decoratePublicProduct failed: %v", err)
	}
	if item.PromotionPriceAmount == nil {
		t.Fatalf("expected promotion price amount")
	}

	expectedDisplay := decimal.RequireFromString("89.90")
	expectedPromotion := decimal.RequireFromString("79.90")
	if !item.PriceAmount.Decimal.Equal(expectedDisplay) {
		t.Fatalf("expected display price %s, got: %s", expectedDisplay.String(), item.PriceAmount.String())
	}
	if !item.PromotionPriceAmount.Decimal.Equal(expectedPromotion) {
		t.Fatalf("expected promotion display price %s, got: %s", expectedPromotion.String(), item.PromotionPriceAmount.String())
	}
}

func TestDecoratePublicProductForTenantUsesResellerPricesAndHidesMainDiscounts(t *testing.T) {
	dsn := fmt.Sprintf("file:public_product_reseller_price_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.ResellerProfile{}, &models.ResellerProductSetting{}, &models.Promotion{}); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	owner := models.User{Email: "public-reseller-owner@example.com", PasswordHash: "hash"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner failed: %v", err)
	}
	profile := models.ResellerProfile{
		UserID:               owner.ID,
		Status:               models.ResellerProfileStatusActive,
		DefaultMarkupPercent: models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
	}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile failed: %v", err)
	}
	product := &models.Product{
		ID:          1,
		Slug:        "reseller-display",
		PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		WholesalePrices: models.WholesalePriceTiers{
			{MinQuantity: 5, UnitPrice: models.NewMoneyFromDecimal(decimal.NewFromInt(80))},
		},
		SKUs: []models.ProductSKU{
			{
				ID:              11,
				ProductID:       1,
				SKUCode:         "VISIBLE",
				IsActive:        true,
				SortOrder:       100,
				PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
				CostPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(50)),
			},
			{
				ID:              12,
				ProductID:       1,
				SKUCode:         "HIDDEN",
				IsActive:        true,
				SortOrder:       10,
				PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(90)),
				CostPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(50)),
			},
		},
	}
	hiddenSetting := models.ResellerProductSetting{
		ResellerID:  profile.ID,
		ProductID:   product.ID,
		SKUID:       12,
		IsListed:    false,
		PricingMode: models.ResellerPricingModeInherit,
	}
	if err := db.Create(&hiddenSetting).Error; err != nil {
		t.Fatalf("create hidden sku setting failed: %v", err)
	}
	if err := db.Model(&models.ResellerProductSetting{}).Where("id = ?", hiddenSetting.ID).Update("is_listed", false).Error; err != nil {
		t.Fatalf("force hidden sku setting failed: %v", err)
	}
	fixedPrice := models.ResellerProductSetting{
		ResellerID:       profile.ID,
		ProductID:        product.ID,
		SKUID:            11,
		IsListed:         true,
		PricingMode:      models.ResellerPricingModeFixedPrice,
		FixedPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
	}
	if err := db.Create(&fixedPrice).Error; err != nil {
		t.Fatalf("create fixed price setting failed: %v", err)
	}

	promotion := models.Promotion{
		Name:       "main-promo",
		ScopeType:  constants.ScopeTypeProduct,
		ScopeRefID: product.ID,
		Type:       constants.PromotionTypeFixed,
		Value:      models.NewMoneyFromDecimal(decimal.NewFromInt(20)),
		MinAmount:  models.NewMoneyFromDecimal(decimal.Zero),
		IsActive:   true,
	}
	if err := db.Create(&promotion).Error; err != nil {
		t.Fatalf("create promotion failed: %v", err)
	}

	resolver := service.NewResellerPricingResolver(repository.NewResellerRepository(db))
	h := &Handler{Container: &provider.Container{ResellerPricingResolver: resolver}}
	tenant := service.ResellerTenantContext("shop.example.test", profile.ID, owner.ID, "shop.example.test")
	batch, err := resolver.LoadDisplayPricingBatch(tenant, []models.Product{*product})
	if err != nil {
		t.Fatalf("LoadDisplayPricingBatch failed: %v", err)
	}
	promoService := service.NewPromotionService(repository.NewPromotionRepository(db))

	item, err := h.decoratePublicProductForTenant(product, promoService, tenant, batch)
	if err != nil {
		t.Fatalf("decoratePublicProductForTenant failed: %v", err)
	}
	if !item.PriceAmount.Decimal.Equal(decimal.NewFromInt(130)) {
		t.Fatalf("expected reseller product price 130, got %s", item.PriceAmount.String())
	}
	if len(item.SKUs) != 1 || item.SKUs[0].ID != 11 {
		t.Fatalf("expected only visible sku 11, got %+v", item.SKUs)
	}
	if !item.SKUs[0].PriceAmount.Decimal.Equal(decimal.NewFromInt(130)) {
		t.Fatalf("expected reseller sku price 130, got %s", item.SKUs[0].PriceAmount.String())
	}
	if item.PromotionPriceAmount != nil || item.PromotionID != nil || len(item.PromotionRules) > 0 {
		t.Fatalf("reseller display must not expose main promotion fields: %+v", item)
	}
	if len(item.WholesalePrices) > 0 {
		t.Fatalf("reseller display must not expose main wholesale prices: %+v", item.WholesalePrices)
	}
}

func TestDecoratePublicProductForTenantHiddenProduct(t *testing.T) {
	repo := &resellerPricingRepoForPublicTest{
		profile: &models.ResellerProfile{ID: 10, UserID: 99, Status: models.ResellerProfileStatusActive},
		settings: []models.ResellerProductSetting{
			{ID: 1, ResellerID: 10, ProductID: 1, SKUID: 0, IsListed: false, PricingMode: models.ResellerPricingModeInherit},
		},
	}
	resolver := service.NewResellerPricingResolver(repo)
	h := &Handler{Container: &provider.Container{ResellerPricingResolver: resolver}}
	tenant := service.ResellerTenantContext("shop.example.test", 10, 99, "shop.example.test")
	product := &models.Product{
		ID:          1,
		PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		SKUs: []models.ProductSKU{
			{ID: 11, ProductID: 1, IsActive: true, PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)), CostPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(50))},
		},
	}
	batch, err := resolver.LoadDisplayPricingBatch(tenant, []models.Product{*product})
	if err != nil {
		t.Fatalf("LoadDisplayPricingBatch failed: %v", err)
	}

	_, err = h.decoratePublicProductForTenant(product, nil, tenant, batch)
	if err != service.ErrResellerProductNotListed {
		t.Fatalf("expected ErrResellerProductNotListed, got %v", err)
	}
}

func TestDecoratePublicProductForTenantInvalidDisplayPricingIsHidden(t *testing.T) {
	tests := []struct {
		name    string
		profile *models.ResellerProfile
		setting models.ResellerProductSetting
		cost    decimal.Decimal
	}{
		{
			name:    "below base",
			profile: &models.ResellerProfile{ID: 10, UserID: 99, Status: models.ResellerProfileStatusActive},
			setting: models.ResellerProductSetting{
				ID:               1,
				ResellerID:       10,
				ProductID:        1,
				SKUID:            11,
				IsListed:         true,
				PricingMode:      models.ResellerPricingModeFixedPrice,
				FixedPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(90)),
			},
			cost: decimal.NewFromInt(50),
		},
		{
			name:    "markup exceeds max",
			profile: &models.ResellerProfile{ID: 10, UserID: 99, Status: models.ResellerProfileStatusActive, MaxMarkupPercent: models.NewMoneyFromDecimal(decimal.NewFromInt(10))},
			setting: models.ResellerProductSetting{
				ID:               2,
				ResellerID:       10,
				ProductID:        1,
				SKUID:            11,
				IsListed:         true,
				PricingMode:      models.ResellerPricingModeFixedPrice,
				FixedPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(120)),
			},
			cost: decimal.NewFromInt(50),
		},
		{
			name:    "unknown pricing mode",
			profile: &models.ResellerProfile{ID: 10, UserID: 99, Status: models.ResellerProfileStatusActive},
			setting: models.ResellerProductSetting{
				ID:          3,
				ResellerID:  10,
				ProductID:   1,
				SKUID:       11,
				IsListed:    true,
				PricingMode: "surprise",
			},
			cost: decimal.NewFromInt(50),
		},
		{
			name:    "below cost",
			profile: &models.ResellerProfile{ID: 10, UserID: 99, Status: models.ResellerProfileStatusActive},
			setting: models.ResellerProductSetting{
				ID:          4,
				ResellerID:  10,
				ProductID:   1,
				SKUID:       11,
				IsListed:    true,
				PricingMode: models.ResellerPricingModeInherit,
			},
			cost: decimal.NewFromInt(120),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &resellerPricingRepoForPublicTest{
				profile:  tt.profile,
				settings: []models.ResellerProductSetting{tt.setting},
			}
			resolver := service.NewResellerPricingResolver(repo)
			h := &Handler{Container: &provider.Container{ResellerPricingResolver: resolver}}
			tenant := service.ResellerTenantContext("shop.example.test", 10, 99, "shop.example.test")
			product := &models.Product{
				ID:          1,
				PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
				SKUs: []models.ProductSKU{
					{ID: 11, ProductID: 1, IsActive: true, PriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(100)), CostPriceAmount: models.NewMoneyFromDecimal(tt.cost)},
				},
			}
			batch, err := resolver.LoadDisplayPricingBatch(tenant, []models.Product{*product})
			if err != nil {
				t.Fatalf("LoadDisplayPricingBatch failed: %v", err)
			}

			_, err = h.decoratePublicProductForTenant(product, nil, tenant, batch)
			if !errors.Is(err, service.ErrResellerProductNotListed) {
				t.Fatalf("expected ErrResellerProductNotListed, got %v", err)
			}
		})
	}
}

type resellerPricingRepoForPublicTest struct {
	profile  *models.ResellerProfile
	settings []models.ResellerProductSetting
}

func (r *resellerPricingRepoForPublicTest) Transaction(fn func(tx *gorm.DB) error) error {
	return fn(nil)
}

func (r *resellerPricingRepoForPublicTest) WithTx(tx *gorm.DB) repository.ResellerRepository {
	return r
}

func (r *resellerPricingRepoForPublicTest) CreateProfile(profile *models.ResellerProfile) error {
	r.profile = profile
	return nil
}

func (r *resellerPricingRepoForPublicTest) GetProfileByID(id uint) (*models.ResellerProfile, error) {
	if r.profile == nil || r.profile.ID != id {
		return nil, nil
	}
	profile := *r.profile
	return &profile, nil
}

func (r *resellerPricingRepoForPublicTest) GetProfileByUserID(userID uint) (*models.ResellerProfile, error) {
	return nil, nil
}

func (r *resellerPricingRepoForPublicTest) UpsertDomain(domain models.ResellerDomain) (*models.ResellerDomain, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *resellerPricingRepoForPublicTest) FindDomainByHost(host string) (*models.ResellerDomain, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *resellerPricingRepoForPublicTest) FindActiveVerifiedDomain(host string) (*models.ResellerDomain, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *resellerPricingRepoForPublicTest) UpsertSiteConfig(config models.ResellerSiteConfig) (*models.ResellerSiteConfig, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *resellerPricingRepoForPublicTest) ListProductSettingsForPricing(resellerID uint, productIDs []uint, skuIDs []uint) ([]models.ResellerProductSetting, error) {
	return append([]models.ResellerProductSetting(nil), r.settings...), nil
}

func (r *resellerPricingRepoForPublicTest) ListHiddenProductIDs(resellerID uint) ([]uint, error) {
	return nil, nil
}

func (r *resellerPricingRepoForPublicTest) IsActiveRelatedAccount(resellerID uint, userID uint) (bool, error) {
	return false, nil
}

func (r *resellerPricingRepoForPublicTest) CreateOrderSnapshot(snapshot *models.ResellerOrderSnapshot) error {
	return nil
}

func (r *resellerPricingRepoForPublicTest) GetOrderSnapshotByOrderID(orderID uint) (*models.ResellerOrderSnapshot, error) {
	return nil, nil
}
