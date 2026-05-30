package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/provider"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupAdminProductHandlerTest(t *testing.T) (*Handler, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:admin_product_handler_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Category{},
		&models.Product{},
		&models.ProductSKU{},
		&models.CardSecret{},
		&models.CardSecretBatch{},
		&models.MemberLevelPrice{},
		&models.CartItem{},
		&models.ProductMapping{},
		&models.SKUMapping{},
		&models.Order{},
		&models.OrderItem{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	productService := service.NewProductService(
		repository.NewProductRepository(db),
		repository.NewProductSKURepository(db),
		repository.NewCardSecretRepository(db),
		repository.NewCardSecretBatchRepository(db),
		repository.NewCategoryRepository(db),
		repository.NewMemberLevelPriceRepository(db),
		repository.NewCartRepository(db),
		repository.NewProductMappingRepository(db),
		repository.NewOrderRepository(db),
		repository.NewPaymentChannelRepository(db),
	)

	h := &Handler{Container: &provider.Container{
		ProductService: productService,
	}}
	return h, db
}

func TestBatchUpdateProductStatusReturnsFailureReasons(t *testing.T) {
	h, db := setupAdminProductHandlerTest(t)

	product := models.Product{
		CategoryID:      0,
		Slug:            "batch-uncategorized-product",
		TitleJSON:       models.JSON{"zh-CN": "batch-uncategorized-product"},
		PriceAmount:     models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
		FulfillmentType: constants.FulfillmentTypeUpstream,
		IsMapped:        true,
		IsActive:        false,
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatalf("create uncategorized product failed: %v", err)
	}

	body := fmt.Sprintf(`{"ids":[%d],"is_active":true}`, product.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/products/batch-status", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.BatchUpdateProductStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Total        int `json:"total"`
			SuccessCount int `json:"success_count"`
			FailedItems  []struct {
				ID        uint   `json:"id"`
				ErrorCode string `json:"error_code"`
				Message   string `json:"message"`
			} `json:"failed_items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, w.Body.String())
	}
	if resp.Data.Total != 1 || resp.Data.SuccessCount != 0 {
		t.Fatalf("unexpected counts: total=%d success=%d", resp.Data.Total, resp.Data.SuccessCount)
	}
	if len(resp.Data.FailedItems) != 1 {
		t.Fatalf("expected one failed item, got %+v", resp.Data.FailedItems)
	}
	if resp.Data.FailedItems[0].ID != product.ID {
		t.Fatalf("expected failed product id %d, got %d", product.ID, resp.Data.FailedItems[0].ID)
	}
	if resp.Data.FailedItems[0].ErrorCode != "product_category_invalid" {
		t.Fatalf("expected product_category_invalid, got %q", resp.Data.FailedItems[0].ErrorCode)
	}
}
