package provider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/payment/huifu"
)

func TestHuifuAdapterHostingDataUsesConfiguredProjectFields(t *testing.T) {
	got := buildHuifuHostingData(&huifu.Config{
		ProjectTitle: "录入项目标题",
		ProjectID:    "PROJECTID202605310001",
		CallbackURL:  "https://shop.example.com/pay/return",
	}, "https://shop.example.com/pay?guest=1&order_no=ORDER-1")

	if got["project_title"] != "录入项目标题" {
		t.Fatalf("project_title = %v", got["project_title"])
	}
	if got["project_id"] != "PROJECTID202605310001" {
		t.Fatalf("project_id = %v", got["project_id"])
	}
	if got["callback_url"] != "https://shop.example.com/pay?guest=1&order_no=ORDER-1" {
		t.Fatalf("callback_url = %v", got["callback_url"])
	}
}

func TestHuifuAdapterHostingDataFallsBackToConfiguredCallback(t *testing.T) {
	got := buildHuifuHostingData(&huifu.Config{
		ProjectTitle: "录入项目标题",
		ProjectID:    "PROJECTID202605310001",
		CallbackURL:  "https://shop.example.com/pay/return",
	}, "")

	if got["callback_url"] != "https://shop.example.com/pay/return" {
		t.Fatalf("callback_url = %v", got["callback_url"])
	}
}

func TestHuifuAdapterHostingDataFallsBackToReturnURLCallback(t *testing.T) {
	got := buildHuifuHostingData(&huifu.Config{
		ProjectTitle: "录入项目标题",
		ProjectID:    "PROJECTID202605310001",
	}, "https://shop.example.com/pay?order_no=ORDER-1")

	if got["callback_url"] != "https://shop.example.com/pay?order_no=ORDER-1" {
		t.Fatalf("callback_url = %v", got["callback_url"])
	}
}

func TestHuifuAdapterQueryPaymentMapsResult(t *testing.T) {
	privatePEM, publicPEM := testHuifuAdapterKeyPair(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := []byte(`{"resp_code":"00000000","org_req_seq_id":"DJP20260531140037562501","hf_seq_id":"HFQ123","trans_stat":"S","trans_amt":"0.01","trans_time":"20260531140103"}`)
		sign, err := huifu.Sign(data, privatePEM)
		if err != nil {
			t.Fatalf("sign response failed: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"resp_code": "00000000",
			"sign":      sign,
			"data":      string(data),
		})
	}))
	defer srv.Close()

	adapter := NewHuifuAdapter().(interface {
		QueryPayment(context.Context, models.JSON, string) (*QueryResult, error)
	})
	result, err := adapter.QueryPayment(context.Background(), models.JSON{
		"gateway_url":         srv.URL,
		"sys_id":              "6666000200643505",
		"product_id":          "PAY_UNION",
		"huifu_id":            "6666000200643505",
		"private_key":         privatePEM,
		"platform_public_key": publicPEM,
		"notify_url":          "https://shop.example.com/api/v1/payments/callback",
		"return_url":          "https://shop.example.com/pay",
	}, "DJP20260531140037562501")
	if err != nil {
		t.Fatalf("QueryPayment failed: %v", err)
	}
	if result.Status != constants.PaymentStatusSuccess {
		t.Fatalf("status = %s", result.Status)
	}
	if result.ProviderRef != "HFQ123" {
		t.Fatalf("provider_ref = %s", result.ProviderRef)
	}
	if result.Amount.String() != "0.01" || result.Currency != constants.SiteCurrencyDefault {
		t.Fatalf("amount/currency = %s %s", result.Amount.String(), result.Currency)
	}
	if result.PaidAt == nil {
		t.Fatalf("paid_at should be parsed")
	}
}

func testHuifuAdapterKeyPair(t *testing.T) (string, string) {
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
