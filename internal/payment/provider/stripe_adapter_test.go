package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/payment/stripe"

	"github.com/shopspring/decimal"
)

func TestStripeAdapter_Type(t *testing.T) {
	a := NewStripeAdapter()
	want := constants.PaymentProviderOfficial + ":" + constants.PaymentChannelTypeStripe
	if got := a.Type(); got != want {
		t.Fatalf("Type() = %q, want %q", got, want)
	}
}

func TestStripeAdapter_ValidateConfig_InvalidIsMapped(t *testing.T) {
	a := NewStripeAdapter()
	// 缺 secret_key，应被 stripe.ValidateConfig 拒绝
	raw := models.JSON{
		"webhook_secret":       "whsec_x",
		"success_url":          "https://example.com/s",
		"cancel_url":           "https://example.com/c",
		"api_base_url":         "https://api.stripe.com",
		"payment_method_types": []any{"card"},
	}
	err := a.ValidateConfig(raw, "redirect")
	if err == nil {
		t.Fatalf("expected error for missing secret_key")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("expected wrapped ErrConfigInvalid, got %v", err)
	}
}

func TestStripeAdapter_CreatePayment_ConfigInvalidMapped(t *testing.T) {
	a := NewStripeAdapter()
	raw := models.JSON{} // 空 config
	_, err := a.CreatePayment(context.Background(), raw, CreateInput{
		OrderNo:  "ORDER_1",
		Currency: "USD",
	})
	if err == nil {
		t.Fatalf("expected error from empty config")
	}
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("expected wrapped ErrConfigInvalid, got %v", err)
	}
}

// TestStripeAdapter_CreatePayment_ExchangeRate_AuditFields 守护 P1.2c e6bc261
// silent regression fix:当 stripe config 配 target_currency + exchange_rate 时,
// wrapper 必须把转换信息写入 result.Payload(exchange_rate / original_amount /
// original_currency)并把 AmountSent / CurrencySent 设为转换后数值。
//
// 该模式是 alipay/wechatpay/stripe/paypal/epay 5 个跨币种 wrapper 的共享代码
// 路径(都用 common.ExchangeRateConfig.NeedsCurrencyConversion + ConvertAmount),
// 任何一个 wrapper 的此块被删除/破坏都会导致跨币种支付审计字段丢失。
func TestStripeAdapter_CreatePayment_ExchangeRate_AuditFields(t *testing.T) {
	// mock stripe /v1/checkout/sessions 成功响应:返回最小可用的 session 对象
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_audit_001","url":"https://checkout.stripe.com/pay/cs_test_audit_001","status":"open"}`))
	}))
	defer server.Close()

	a := NewStripeAdapter()
	raw := models.JSON{
		"secret_key":           "sk_test_abc",
		"webhook_secret":       "whsec_xyz",
		"success_url":          "https://shop.example.com/success",
		"cancel_url":           "https://shop.example.com/cancel",
		"api_base_url":         server.URL,
		"payment_method_types": []any{"card"},
		// 跨币种:10 USD → 72 CNY (rate 7.2)
		"target_currency": "CNY",
		"exchange_rate":   "7.2",
	}

	input := CreateInput{
		OrderNo:   "ORDER-USD-10",
		Subject:   "audit field test",
		Currency:  "USD",
		Amount:    models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
		ReturnURL: "https://shop.example.com/pay",
	}

	result, err := a.CreatePayment(context.Background(), raw, input)
	if err != nil {
		t.Fatalf("CreatePayment() failed: %v", err)
	}

	if result.CurrencySent != "CNY" {
		t.Fatalf("CurrencySent = %q, want CNY (converted target)", result.CurrencySent)
	}
	if result.AmountSent != "72" {
		t.Fatalf("AmountSent = %q, want 72 (10 USD * 7.2)", result.AmountSent)
	}

	if got := result.Payload["exchange_rate"]; got != "7.2" {
		t.Fatalf("Payload[exchange_rate] = %v, want 7.2", got)
	}
	if got := result.Payload["original_amount"]; got != "10" {
		t.Fatalf("Payload[original_amount] = %v, want 10", got)
	}
	if got := result.Payload["original_currency"]; got != "USD" {
		t.Fatalf("Payload[original_currency] = %v, want USD", got)
	}
}

// TestStripeAdapter_CreatePayment_NoExchangeRate_AmountSentEqualsOriginal 验证
// 无跨币种配置时,wrapper 不写 audit 字段,AmountSent/CurrencySent 等于原值。
func TestStripeAdapter_CreatePayment_NoExchangeRate_AmountSentEqualsOriginal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_noconv_001","url":"https://checkout.stripe.com/pay/cs_test_noconv_001","status":"open"}`))
	}))
	defer server.Close()

	a := NewStripeAdapter()
	raw := models.JSON{
		"secret_key":           "sk_test_abc",
		"webhook_secret":       "whsec_xyz",
		"success_url":          "https://shop.example.com/success",
		"cancel_url":           "https://shop.example.com/cancel",
		"api_base_url":         server.URL,
		"payment_method_types": []any{"card"},
		// 无 target_currency / exchange_rate 配置
	}

	input := CreateInput{
		OrderNo:   "ORDER-USD-NOCONV",
		Subject:   "no conversion test",
		Currency:  "USD",
		Amount:    models.NewMoneyFromDecimal(decimal.NewFromInt(20)),
		ReturnURL: "https://shop.example.com/pay",
	}

	result, err := a.CreatePayment(context.Background(), raw, input)
	if err != nil {
		t.Fatalf("CreatePayment() failed: %v", err)
	}

	if result.CurrencySent != "USD" {
		t.Fatalf("CurrencySent = %q, want USD (no conversion)", result.CurrencySent)
	}
	if result.AmountSent != "20" {
		t.Fatalf("AmountSent = %q, want 20 (no conversion)", result.AmountSent)
	}

	if _, ok := result.Payload["exchange_rate"]; ok {
		t.Fatal("Payload should NOT contain exchange_rate when no conversion configured")
	}
	if _, ok := result.Payload["original_amount"]; ok {
		t.Fatal("Payload should NOT contain original_amount when no conversion configured")
	}
}

func TestStripeAdapter_MapStripeError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"config", stripe.ErrConfigInvalid, ErrConfigInvalid},
		{"request", stripe.ErrRequestFailed, ErrRequestFailed},
		{"response", stripe.ErrResponseInvalid, ErrResponseInvalid},
		{"signature", stripe.ErrSignatureInvalid, ErrSignatureInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapStripeError(tc.in)
			if !errors.Is(got, tc.want) {
				t.Fatalf("mapStripeError(%v) errors.Is %v = false, want true", tc.in, tc.want)
			}
		})
	}
}
