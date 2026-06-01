package public

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/payment/huifu"
	paymentprovider "github.com/dujiao-next/internal/payment/provider"
	"github.com/dujiao-next/internal/provider"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type huifuCallbackFixture struct {
	orderRepo   repository.OrderRepository
	paymentRepo repository.PaymentRepository
	handler     *Handler
	order       *models.Order
	payment     *models.Payment
	privatePEM  string
}

func newHuifuCallbackFixture(t *testing.T) *huifuCallbackFixture {
	t.Helper()

	gin.SetMode(gin.TestMode)
	privatePEM, publicPEM := huifuCallbackKeyPair(t)

	dsn := fmt.Sprintf("file:payment_callback_huifu_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.User{},
		&models.Product{},
		&models.ProductSKU{},
		&models.Order{},
		&models.OrderItem{},
		&models.Fulfillment{},
		&models.PaymentChannel{},
		&models.Payment{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	user := &models.User{
		Email:        "huifu-callback@example.com",
		PasswordHash: "hash",
		Status:       constants.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	order := &models.Order{
		OrderNo:                 "DJHUIFUCALLBACK001",
		UserID:                  user.ID,
		Status:                  constants.OrderStatusPendingPayment,
		Currency:                constants.SiteCurrencyDefault,
		OriginalAmount:          models.NewMoneyFromDecimal(decimal.NewFromFloat(0.01)),
		DiscountAmount:          models.NewMoneyFromDecimal(decimal.Zero),
		PromotionDiscountAmount: models.NewMoneyFromDecimal(decimal.Zero),
		TotalAmount:             models.NewMoneyFromDecimal(decimal.NewFromFloat(0.01)),
		WalletPaidAmount:        models.NewMoneyFromDecimal(decimal.Zero),
		OnlinePaidAmount:        models.NewMoneyFromDecimal(decimal.NewFromFloat(0.01)),
		RefundedAmount:          models.NewMoneyFromDecimal(decimal.Zero),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	channel := &models.PaymentChannel{
		Name:            "Huifu",
		ProviderType:    constants.PaymentProviderHuifu,
		ChannelType:     constants.PaymentChannelTypeCashier,
		InteractionMode: constants.PaymentInteractionRedirect,
		FeeRate:         models.NewMoneyFromDecimal(decimal.Zero),
		FixedFee:        models.NewMoneyFromDecimal(decimal.Zero),
		ConfigJSON: models.JSON{
			"gateway_url":         "https://api.huifu.com",
			"sys_id":              "6666000200643505",
			"product_id":          "PAY_UNION",
			"huifu_id":            "6666000200643505",
			"private_key":         privatePEM,
			"platform_public_key": publicPEM,
			"notify_url":          "https://shop.example.com/api/v1/payments/callback",
			"return_url":          "https://shop.example.com/pay",
		},
		IsActive:  true,
		SortOrder: 10,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel failed: %v", err)
	}
	payment := &models.Payment{
		OrderID:         order.ID,
		ChannelID:       channel.ID,
		ProviderType:    channel.ProviderType,
		ChannelType:     channel.ChannelType,
		InteractionMode: channel.InteractionMode,
		Amount:          models.NewMoneyFromDecimal(decimal.NewFromFloat(0.01)),
		FeeRate:         models.NewMoneyFromDecimal(decimal.Zero),
		FeeAmount:       models.NewMoneyFromDecimal(decimal.Zero),
		Currency:        constants.SiteCurrencyDefault,
		Status:          constants.PaymentStatusPending,
		ProviderRef:     "DJP20260531140037562501",
		GatewayOrderNo:  "DJP20260531140037562501",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(payment).Error; err != nil {
		t.Fatalf("create payment failed: %v", err)
	}

	orderRepo := repository.NewOrderRepository(db)
	paymentRepo := repository.NewPaymentRepository(db)
	channelRepo := repository.NewPaymentChannelRepository(db)
	productRepo := repository.NewProductRepository(db)
	productSKURepo := repository.NewProductSKURepository(db)

	registry := paymentprovider.NewRegistry()
	registry.Register(constants.PaymentProviderHuifu, "", paymentprovider.NewHuifuAdapter())

	paymentService := service.NewPaymentService(service.PaymentServiceOptions{
		OrderRepo:               orderRepo,
		ProductRepo:             productRepo,
		ProductSKURepo:          productSKURepo,
		PaymentRepo:             paymentRepo,
		ChannelRepo:             channelRepo,
		ExpireMinutes:           15,
		PaymentProviderRegistry: registry,
	})

	return &huifuCallbackFixture{
		orderRepo:   orderRepo,
		paymentRepo: paymentRepo,
		handler: &Handler{Container: &provider.Container{
			OrderRepo:          orderRepo,
			PaymentRepo:        paymentRepo,
			PaymentChannelRepo: channelRepo,
			PaymentService:     paymentService,
		}},
		order:      order,
		payment:    payment,
		privatePEM: privatePEM,
	}
}

func TestPaymentCallbackHandlesHuifuFormCallback(t *testing.T) {
	fixture := newHuifuCallbackFixture(t)
	data := `{"req_seq_id":"DJP20260531140037562501","hf_seq_id":"HF123","trans_stat":"S","trans_amt":"0.01","trans_time":"20260531140103"}`
	sign, err := huifu.Sign([]byte(data), fixture.privatePEM)
	if err != nil {
		t.Fatalf("sign callback data failed: %v", err)
	}
	form := url.Values{}
	form.Set("sign", sign)
	form.Set("data", data)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/callback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	fixture.handler.PaymentCallback(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != constants.HuifuCallbackSuccess {
		t.Fatalf("unexpected response body: %s", w.Body.String())
	}

	updatedPayment, err := fixture.paymentRepo.GetByID(fixture.payment.ID)
	if err != nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if updatedPayment == nil || updatedPayment.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status not updated: %+v", updatedPayment)
	}
	updatedOrder, err := fixture.orderRepo.GetByID(fixture.order.ID)
	if err != nil {
		t.Fatalf("reload order failed: %v", err)
	}
	if updatedOrder == nil || updatedOrder.Status != constants.OrderStatusPaid {
		t.Fatalf("order status not updated: %+v", updatedOrder)
	}
}

func TestPaymentCallbackHandlesHuifuFlatFormCallback(t *testing.T) {
	fixture := newHuifuCallbackFixture(t)
	data, err := json.Marshal(map[string]interface{}{
		"req_seq_id": "DJP20260531140037562501",
		"hf_seq_id":  "HF123",
		"trans_stat": "S",
		"trans_amt":  "0.01",
		"trans_time": "20260531140103",
	})
	if err != nil {
		t.Fatalf("marshal callback data failed: %v", err)
	}
	sign, err := huifu.Sign(data, fixture.privatePEM)
	if err != nil {
		t.Fatalf("sign callback data failed: %v", err)
	}
	form := url.Values{}
	form.Set("sign", sign)
	form.Set("req_seq_id", "DJP20260531140037562501")
	form.Set("hf_seq_id", "HF123")
	form.Set("trans_stat", "S")
	form.Set("trans_amt", "0.01")
	form.Set("trans_time", "20260531140103")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments/callback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	fixture.handler.PaymentCallback(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != constants.HuifuCallbackSuccess {
		t.Fatalf("unexpected response body: %s", w.Body.String())
	}
	updatedPayment, err := fixture.paymentRepo.GetByID(fixture.payment.ID)
	if err != nil {
		t.Fatalf("reload payment failed: %v", err)
	}
	if updatedPayment == nil || updatedPayment.Status != constants.PaymentStatusSuccess {
		t.Fatalf("payment status not updated: %+v", updatedPayment)
	}
}

func huifuCallbackKeyPair(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key failed: %v", err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	publicBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key failed: %v", err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicBytes})
	return string(privatePEM), string(publicPEM)
}
