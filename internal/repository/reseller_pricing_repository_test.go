package repository

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func openResellerPricingRepoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:reseller_pricing_repo_%d?mode=memory&cache=shared", time.Now().UnixNano())
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
		&models.ResellerProfile{},
		&models.ResellerProductSetting{},
		&models.ResellerRelatedAccount{},
		&models.ResellerOrderSnapshot{},
	); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	return db
}

func seedResellerPricingProduct(t *testing.T, db *gorm.DB, slug string, activeSKUCount int) (models.Product, []models.ProductSKU) {
	t.Helper()
	var category models.Category
	if err := db.FirstOrCreate(&category, models.Category{
		Slug:     "repo-pricing",
		NameJSON: models.JSON{"zh-CN": "repo-pricing"},
		IsActive: true,
	}).Error; err != nil {
		t.Fatalf("seed category failed: %v", err)
	}
	product := models.Product{
		CategoryID:      category.ID,
		Slug:            slug,
		TitleJSON:       models.JSON{"zh-CN": slug},
		PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		PurchaseType:    constants.ProductPurchaseMember,
		FulfillmentType: constants.FulfillmentTypeManual,
		IsActive:        true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create product %s failed: %v", slug, err)
	}
	skus := make([]models.ProductSKU, 0, activeSKUCount)
	for i := 0; i < activeSKUCount; i++ {
		sku := models.ProductSKU{
			ProductID:        product.ID,
			SKUCode:          fmt.Sprintf("%s-sku-%d", slug, i+1),
			PriceAmount:      models.NewMoneyFromDecimal(decimal.NewFromInt(100 + int64(i))),
			CostPriceAmount:  models.NewMoneyFromDecimal(decimal.NewFromInt(50)),
			ManualStockTotal: constants.ManualStockUnlimited,
			IsActive:         true,
			SortOrder:        activeSKUCount - i,
			CreatedAt:        time.Now(),
			UpdatedAt:        time.Now(),
		}
		if err := db.Create(&sku).Error; err != nil {
			t.Fatalf("create sku for %s failed: %v", slug, err)
		}
		skus = append(skus, sku)
	}
	return product, skus
}

func TestResellerPricingRepositoryProductSettingsAndHiddenProducts(t *testing.T) {
	db := openResellerPricingRepoTestDB(t)
	profile := seedResellerProfile(t, db, "pricing-owner@example.com")
	otherProfile := seedResellerProfile(t, db, "pricing-other@example.com")
	repo := NewResellerRepository(db)

	listedProduct, listedSKUs := seedResellerPricingProduct(t, db, "listed", 2)
	productHidden, _ := seedResellerPricingProduct(t, db, "product-hidden", 1)
	allSKUHidden, allHiddenSKUs := seedResellerPricingProduct(t, db, "all-sku-hidden", 2)
	partialSKUHidden, partialHiddenSKUs := seedResellerPricingProduct(t, db, "partial-sku-hidden", 2)
	otherResellerHidden, _ := seedResellerPricingProduct(t, db, "other-reseller-hidden", 1)

	settings := []models.ResellerProductSetting{
		{
			ResellerID:    profile.ID,
			ProductID:     listedProduct.ID,
			SKUID:         0,
			IsListed:      true,
			PricingMode:   models.ResellerPricingModeMarkupPercent,
			MarkupPercent: models.NewMoneyFromDecimal(decimal.NewFromInt(15)),
		},
		{
			ResellerID:       profile.ID,
			ProductID:        listedProduct.ID,
			SKUID:            listedSKUs[0].ID,
			IsListed:         true,
			PricingMode:      models.ResellerPricingModeFixedPrice,
			FixedPriceAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		},
		{
			ResellerID:  profile.ID,
			ProductID:   productHidden.ID,
			SKUID:       0,
			IsListed:    false,
			PricingMode: models.ResellerPricingModeInherit,
		},
		{
			ResellerID:  profile.ID,
			ProductID:   allSKUHidden.ID,
			SKUID:       allHiddenSKUs[0].ID,
			IsListed:    false,
			PricingMode: models.ResellerPricingModeInherit,
		},
		{
			ResellerID:  profile.ID,
			ProductID:   allSKUHidden.ID,
			SKUID:       allHiddenSKUs[1].ID,
			IsListed:    false,
			PricingMode: models.ResellerPricingModeInherit,
		},
		{
			ResellerID:  profile.ID,
			ProductID:   partialSKUHidden.ID,
			SKUID:       partialHiddenSKUs[0].ID,
			IsListed:    false,
			PricingMode: models.ResellerPricingModeInherit,
		},
		{
			ResellerID:  otherProfile.ID,
			ProductID:   otherResellerHidden.ID,
			SKUID:       0,
			IsListed:    false,
			PricingMode: models.ResellerPricingModeInherit,
		},
	}
	for i := range settings {
		wantListed := settings[i].IsListed
		if err := db.Create(&settings[i]).Error; err != nil {
			t.Fatalf("create setting %d failed: %v", i, err)
		}
		if !wantListed {
			if err := db.Model(&models.ResellerProductSetting{}).
				Where("id = ?", settings[i].ID).
				Update("is_listed", false).Error; err != nil {
				t.Fatalf("force hidden setting %d failed: %v", i, err)
			}
			settings[i].IsListed = false
		}
	}
	softDeleted := models.ResellerProductSetting{
		ResellerID:    profile.ID,
		ProductID:     listedProduct.ID,
		SKUID:         listedSKUs[1].ID,
		IsListed:      true,
		PricingMode:   models.ResellerPricingModeMarkupPercent,
		MarkupPercent: models.NewMoneyFromDecimal(decimal.NewFromInt(99)),
	}
	if err := db.Select("*").Create(&softDeleted).Error; err != nil {
		t.Fatalf("create soft-deleted setting failed: %v", err)
	}
	if err := db.Delete(&softDeleted).Error; err != nil {
		t.Fatalf("soft delete setting failed: %v", err)
	}

	rows, err := repo.ListProductSettingsForPricing(profile.ID, []uint{listedProduct.ID, productHidden.ID}, []uint{listedSKUs[0].ID, listedSKUs[1].ID})
	if err != nil {
		t.Fatalf("ListProductSettingsForPricing failed: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected product-level, sku-level, and hidden product settings only, got %d: %+v", len(rows), rows)
	}
	gotKeys := make([]string, 0, len(rows))
	for _, row := range rows {
		gotKeys = append(gotKeys, fmt.Sprintf("%d:%d:%t", row.ProductID, row.SKUID, row.IsListed))
	}
	sort.Strings(gotKeys)
	wantKeys := []string{
		fmt.Sprintf("%d:%d:%t", listedProduct.ID, uint(0), true),
		fmt.Sprintf("%d:%d:%t", listedProduct.ID, listedSKUs[0].ID, true),
		fmt.Sprintf("%d:%d:%t", productHidden.ID, uint(0), false),
	}
	sort.Strings(wantKeys)
	if fmt.Sprint(gotKeys) != fmt.Sprint(wantKeys) {
		t.Fatalf("settings mismatch want %v got %v", wantKeys, gotKeys)
	}

	hiddenIDs, err := repo.ListHiddenProductIDs(profile.ID)
	if err != nil {
		t.Fatalf("ListHiddenProductIDs failed: %v", err)
	}
	sort.Slice(hiddenIDs, func(i, j int) bool { return hiddenIDs[i] < hiddenIDs[j] })
	wantHidden := []uint{productHidden.ID, allSKUHidden.ID}
	sort.Slice(wantHidden, func(i, j int) bool { return wantHidden[i] < wantHidden[j] })
	if fmt.Sprint(hiddenIDs) != fmt.Sprint(wantHidden) {
		t.Fatalf("hidden ids mismatch want %v got %v", wantHidden, hiddenIDs)
	}
}

func TestResellerPricingRepositoryRelatedAccountAndSnapshot(t *testing.T) {
	db := openResellerPricingRepoTestDB(t)
	profile := seedResellerProfile(t, db, "snapshot-owner@example.com")
	otherProfile := seedResellerProfile(t, db, "snapshot-other@example.com")
	repo := NewResellerRepository(db)

	activeUser := models.User{Email: "related-active@example.com", PasswordHash: "hash"}
	disabledUser := models.User{Email: "related-disabled@example.com", PasswordHash: "hash"}
	otherUser := models.User{Email: "related-other@example.com", PasswordHash: "hash"}
	if err := db.Create(&activeUser).Error; err != nil {
		t.Fatalf("create active user failed: %v", err)
	}
	if err := db.Create(&disabledUser).Error; err != nil {
		t.Fatalf("create disabled user failed: %v", err)
	}
	if err := db.Create(&otherUser).Error; err != nil {
		t.Fatalf("create other user failed: %v", err)
	}
	relatedRows := []models.ResellerRelatedAccount{
		{ResellerID: profile.ID, UserID: activeUser.ID, RelationType: "manual", Source: "test", Status: models.ResellerRelatedAccountStatusActive},
		{ResellerID: profile.ID, UserID: disabledUser.ID, RelationType: "manual", Source: "test", Status: models.ResellerRelatedAccountStatusDisabled},
		{ResellerID: otherProfile.ID, UserID: otherUser.ID, RelationType: "manual", Source: "test", Status: models.ResellerRelatedAccountStatusActive},
	}
	for i := range relatedRows {
		if err := db.Create(&relatedRows[i]).Error; err != nil {
			t.Fatalf("create related row %d failed: %v", i, err)
		}
	}

	ok, err := repo.IsActiveRelatedAccount(profile.ID, activeUser.ID)
	if err != nil {
		t.Fatalf("IsActiveRelatedAccount active failed: %v", err)
	}
	if !ok {
		t.Fatal("expected active related account")
	}
	ok, err = repo.IsActiveRelatedAccount(profile.ID, disabledUser.ID)
	if err != nil {
		t.Fatalf("IsActiveRelatedAccount disabled failed: %v", err)
	}
	if ok {
		t.Fatal("disabled related account should not match")
	}
	ok, err = repo.IsActiveRelatedAccount(profile.ID, otherUser.ID)
	if err != nil {
		t.Fatalf("IsActiveRelatedAccount other reseller failed: %v", err)
	}
	if ok {
		t.Fatal("other reseller related account should not match")
	}

	order := models.Order{
		OrderNo:     "SNAPSHOT-ORDER",
		UserID:      activeUser.ID,
		Status:      constants.OrderStatusPendingPayment,
		Currency:    "USD",
		TotalAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	snapshot := &models.ResellerOrderSnapshot{
		OrderID:             order.ID,
		ResellerID:          profile.ID,
		Domain:              "primary.example.test",
		Currency:            "USD",
		ResellerUserID:      profile.UserID,
		BuyerUserID:         activeUser.ID,
		BaseAmount:          models.NewMoneyFromDecimal(decimal.NewFromInt(100)),
		ResellerAmount:      models.NewMoneyFromDecimal(decimal.NewFromInt(130)),
		ProfitAmount:        models.NewMoneyFromDecimal(decimal.NewFromInt(30)),
		ProfitEligible:      true,
		PricingSnapshotJSON: models.JSON{"items": []interface{}{map[string]interface{}{"order_item_id": float64(0)}}},
		RiskSnapshotJSON:    models.JSON{"profit_eligible": true},
	}
	if err := repo.CreateOrderSnapshot(snapshot); err != nil {
		t.Fatalf("CreateOrderSnapshot failed: %v", err)
	}
	got, err := repo.GetOrderSnapshotByOrderID(order.ID)
	if err != nil {
		t.Fatalf("GetOrderSnapshotByOrderID failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected snapshot")
	}
	if got.ResellerID != profile.ID || got.Currency != "USD" || got.ProfitAmount.String() != "30.00" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	missing, err := repo.GetOrderSnapshotByOrderID(order.ID + 1000)
	if err != nil {
		t.Fatalf("GetOrderSnapshotByOrderID missing failed: %v", err)
	}
	if missing != nil {
		t.Fatalf("missing snapshot should be nil, got %+v", missing)
	}
}
