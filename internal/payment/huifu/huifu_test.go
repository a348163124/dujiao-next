package huifu

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
)

func TestSignAndVerify(t *testing.T) {
	privatePEM, publicPEM := testRSAKeyPair(t)
	content := []byte(`{"req_seq_id":"P202605310001","trans_amt":"1.23"}`)
	sign, err := Sign(content, privatePEM)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if err := Verify(content, sign, publicPEM); err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if err := Verify([]byte(`{"req_seq_id":"changed"}`), sign, publicPEM); err == nil {
		t.Fatalf("Verify should reject changed content")
	}
}

func TestParseCallbackWithStringData(t *testing.T) {
	privatePEM, publicPEM := testRSAKeyPair(t)
	data := []byte(`{"req_seq_id":"P202605310001","hf_seq_id":"HF123","trans_stat":"S","trans_amt":"9.90","trans_time":"20260531112233"}`)
	sign, err := Sign(data, privatePEM)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	body, err := json.Marshal(map[string]interface{}{
		"sign": sign,
		"data": string(data),
	})
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	callback, err := VerifyCallback(&Config{PlatformPublicKey: publicPEM}, body)
	if err != nil {
		t.Fatalf("VerifyCallback failed: %v", err)
	}
	if callback.ReqSeqID != "P202605310001" || callback.HfSeqID != "HF123" || callback.TransStatus != TransStatusSuccess {
		t.Fatalf("unexpected callback: %+v", callback)
	}
}

func TestParseCallbackWithRespDataObject(t *testing.T) {
	privatePEM, publicPEM := testRSAKeyPair(t)
	data := []byte(`{"req_seq_id":"P202605310001","hf_seq_id":"HF123","trans_stat":"S","trans_amt":"9.90","trans_finish_time":"20260531112233"}`)
	sign, err := Sign(data, privatePEM)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	body := []byte(`{"sign":` + strconv.Quote(sign) + `,"resp_data":` + string(data) + `}`)
	callback, err := VerifyCallback(&Config{PlatformPublicKey: publicPEM}, body)
	if err != nil {
		t.Fatalf("VerifyCallback failed: %v", err)
	}
	if callback.ReqSeqID != "P202605310001" || callback.HfSeqID != "HF123" || callback.TransStatus != TransStatusSuccess {
		t.Fatalf("unexpected callback: %+v", callback)
	}
}

func TestParseFlatFormCallback(t *testing.T) {
	privatePEM, publicPEM := testRSAKeyPair(t)
	formData := map[string][]string{
		"req_seq_id": {"P202605310001"},
		"hf_seq_id":  {"HF123"},
		"trans_stat": {"S"},
		"trans_amt":  {"9.90"},
		"trans_time": {"20260531112233"},
	}
	data, err := marshalJSON(map[string]interface{}{
		"req_seq_id": "P202605310001",
		"hf_seq_id":  "HF123",
		"trans_stat": "S",
		"trans_amt":  "9.90",
		"trans_time": "20260531112233",
	})
	if err != nil {
		t.Fatalf("marshal callback data failed: %v", err)
	}
	sign, err := Sign(data, privatePEM)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	formData["sign"] = []string{sign}
	body, err := FormCallbackBody(formData)
	if err != nil {
		t.Fatalf("FormCallbackBody failed: %v", err)
	}
	callback, err := VerifyCallback(&Config{PlatformPublicKey: publicPEM}, body)
	if err != nil {
		t.Fatalf("VerifyCallback failed: %v", err)
	}
	if callback.ReqSeqID != "P202605310001" || callback.HfSeqID != "HF123" || callback.TransStatus != TransStatusSuccess {
		t.Fatalf("unexpected callback: %+v", callback)
	}
}

func TestCreatePaymentSendsCashierFields(t *testing.T) {
	privatePEM, publicPEM := testRSAKeyPair(t)
	var payload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("Decode request failed: %v", err)
		}
		requestData, ok := payload["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("request data should be an object, got %T", payload["data"])
		}
		requestDataBytes, err := marshalJSON(requestData)
		if err != nil {
			t.Fatalf("marshal request data failed: %v", err)
		}
		if err := Verify(requestDataBytes, payload["sign"].(string), publicPEM); err != nil {
			t.Fatalf("request sign should verify against compact body.data: %v", err)
		}
		data := []byte(`{"resp_code":"00000000","req_seq_id":"DJP202605310001","hf_seq_id":"HF123","jump_url":"https://pay.example.test/cashier"}`)
		sign, err := Sign(data, privatePEM)
		if err != nil {
			t.Fatalf("Sign response failed: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"resp_code": "00000000",
			"sign":      sign,
			"data":      string(data),
		})
	}))
	defer srv.Close()

	_, err := CreatePayment(context.Background(), &Config{
		GatewayURL:        srv.URL,
		SysID:             "6666000200643505",
		ProductID:         "XLSISV",
		HuifuID:           "6666000200643505",
		PrivateKey:        privatePEM,
		PlatformPublicKey: publicPEM,
		NotifyURL:         "https://shop.open-nb.com/api/v1/payments/callback",
		ReturnURL:         "https://shop.open-nb.com/pay",
		BizInfo:           `{"payer_check_wx":{"limit_payer":"ADULT"}}`,
		AcctSplitBunch:    `{"acct_infos":[{"huifu_id":"6666000111546360","div_amt":"0.08"}]}`,
		UsageType:         "1",
	}, CreateInput{
		ReqSeqID:       "DJP202605310001",
		HostingOrderID: "DJP202605310001",
		Amount:         "0.02",
		GoodsDesc:      "无线鼠标静音无声笔记本台式电脑",
		ReturnURL:      "https://shop.open-nb.com/pay?order_no=DJP202605310001",
		ClientIP:       "127.0.0.1",
		HostingData: map[string]interface{}{
			"project_title": "无线鼠标静音无声笔记本台式电脑",
			"project_id":    "1001",
			"callback_url":  "https://shop.open-nb.com/pay?order_no=DJP202605310001",
		},
	})
	if err != nil {
		t.Fatalf("CreatePayment failed: %v", err)
	}
	data, ok := payload["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data should be an object, got %T", payload["data"])
	}
	if data["hosting_order_id"] != "DJP202605310001" {
		t.Fatalf("hosting_order_id = %v", data["hosting_order_id"])
	}
	if data["pre_order_type"] != defaultPreOrderType {
		t.Fatalf("pre_order_type = %v", data["pre_order_type"])
	}
	if _, ok := data["time_expire"]; ok {
		t.Fatalf("time_expire should not be sent by default")
	}
	if _, ok := data["usage_type"]; ok {
		t.Fatalf("usage_type should not be sent by default")
	}
	if data["delay_acct_flag"] != "N" {
		t.Fatalf("delay_acct_flag = %v", data["delay_acct_flag"])
	}
	if _, ok := data["return_url"]; ok {
		t.Fatalf("return_url should not be sent at root data level")
	}
	if _, ok := data["client_ip"]; ok {
		t.Fatalf("client_ip should not be sent at root data level")
	}
	if _, ok := data["acct_split_bunch"].(string); !ok {
		t.Fatalf("acct_split_bunch should be a JSON string, got %T", data["acct_split_bunch"])
	}
	if _, ok := data["biz_info"].(string); !ok {
		t.Fatalf("biz_info should be a JSON string, got %T", data["biz_info"])
	}
	hostingDataText, ok := data["hosting_data"].(string)
	if !ok {
		t.Fatalf("hosting_data should be a JSON string, got %T", data["hosting_data"])
	}
	var hostingData map[string]interface{}
	if err := json.Unmarshal([]byte(hostingDataText), &hostingData); err != nil {
		t.Fatalf("hosting_data should contain JSON object: %v", err)
	}
	if hostingData["project_title"] != "无线鼠标静音无声笔记本台式电脑" {
		t.Fatalf("project_title = %v", hostingData["project_title"])
	}
	if hostingData["project_id"] != "1001" {
		t.Fatalf("project_id = %v", hostingData["project_id"])
	}
	if hostingData["callback_url"] != "https://shop.open-nb.com/pay?order_no=DJP202605310001" {
		t.Fatalf("callback_url = %v", hostingData["callback_url"])
	}
}

func TestNormalizeUsageType(t *testing.T) {
	if got := normalizeUsageType("P"); got != "P" {
		t.Fatalf("normalizeUsageType P = %q", got)
	}
	if got := normalizeUsageType("r"); got != "R" {
		t.Fatalf("normalizeUsageType r = %q", got)
	}
	if got := normalizeUsageType("1"); got != "" {
		t.Fatalf("normalizeUsageType 1 = %q", got)
	}
	if got := normalizeUsageType(""); got != "" {
		t.Fatalf("normalizeUsageType empty = %q", got)
	}
}

func TestFormatTimeExpire(t *testing.T) {
	loc := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 5, 31, 9, 30, 0, 0, loc)
	got, err := formatTimeExpire("300", now, loc)
	if err != nil {
		t.Fatalf("formatTimeExpire duration failed: %v", err)
	}
	if got != "20260531093500" {
		t.Fatalf("duration time_expire = %s", got)
	}
	got, err = formatTimeExpire("20260531111230", now, loc)
	if err != nil {
		t.Fatalf("formatTimeExpire absolute failed: %v", err)
	}
	if got != "20260531111230" {
		t.Fatalf("absolute time_expire = %s", got)
	}
	got, err = formatTimeExpire("", now, loc)
	if err != nil || got != "" {
		t.Fatalf("empty time_expire = %q, %v", got, err)
	}
	utcNow := time.Date(2026, 5, 31, 2, 45, 0, 0, time.UTC)
	got, err = formatTimeExpire("300", utcNow, loc)
	if err != nil {
		t.Fatalf("formatTimeExpire UTC duration failed: %v", err)
	}
	if got != "20260531105000" {
		t.Fatalf("UTC duration time_expire = %s", got)
	}
	if _, err := formatTimeExpire("bad", now, loc); err == nil {
		t.Fatalf("formatTimeExpire should reject invalid value")
	}
}

func TestParseCreateResponse(t *testing.T) {
	privatePEM, publicPEM := testRSAKeyPair(t)
	data := []byte(`{"resp_code":"00000000","req_seq_id":"P202605310001","hf_seq_id":"HF123","jump_url":"https://pay.example.test/cashier"}`)
	sign, err := Sign(data, privatePEM)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	body, err := json.Marshal(map[string]interface{}{
		"resp_code": "00000000",
		"sign":      sign,
		"data":      string(data),
	})
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	result, err := ParseCreateResponse(body, publicPEM)
	if err != nil {
		t.Fatalf("ParseCreateResponse failed: %v", err)
	}
	if result.JumpURL != "https://pay.example.test/cashier" || result.HfSeqID != "HF123" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseQueryResponseWithOrderStat(t *testing.T) {
	privatePEM, publicPEM := testRSAKeyPair(t)
	data := []byte(`{"resp_code":"00000000","req_seq_id":"rQ2021121311173944","org_hf_seq_id":"00290TOP1GR210919004230P853ac13262200000","order_stat":"1","trans_amt":"1.00"}`)
	sign, err := Sign(data, privatePEM)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	body, err := json.Marshal(map[string]interface{}{
		"resp_code": "00000000",
		"sign":      sign,
		"data":      string(data),
	})
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	result, err := ParseQueryResponse(body, publicPEM)
	if err != nil {
		t.Fatalf("ParseQueryResponse failed: %v", err)
	}
	if result.Status != "1" {
		t.Fatalf("status = %q", result.Status)
	}
	if ToPaymentStatus(result.Status) != constants.PaymentStatusSuccess {
		t.Fatalf("payment status = %s", ToPaymentStatus(result.Status))
	}
	if result.HfSeqID != "00290TOP1GR210919004230P853ac13262200000" {
		t.Fatalf("hf_seq_id = %s", result.HfSeqID)
	}
}

func TestQueryPaymentSendsSignedQueryFields(t *testing.T) {
	privatePEM, publicPEM := testRSAKeyPair(t)
	var payload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != queryPath {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("Decode request failed: %v", err)
		}
		requestData, ok := payload["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("request data should be an object, got %T", payload["data"])
		}
		requestDataBytes, err := marshalJSON(requestData)
		if err != nil {
			t.Fatalf("marshal request data failed: %v", err)
		}
		if err := Verify(requestDataBytes, payload["sign"].(string), publicPEM); err != nil {
			t.Fatalf("request sign should verify against compact body.data: %v", err)
		}
		data := []byte(`{"resp_code":"00000000","org_req_seq_id":"DJP20260531140037562501","hf_seq_id":"HFQ123","trans_stat":"S","trans_amt":"0.01","trans_time":"20260531140103"}`)
		sign, err := Sign(data, privatePEM)
		if err != nil {
			t.Fatalf("Sign response failed: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"resp_code": "00000000",
			"sign":      sign,
			"data":      string(data),
		})
	}))
	defer srv.Close()

	result, err := QueryPayment(context.Background(), &Config{
		GatewayURL:        srv.URL,
		SysID:             "6666000200643505",
		ProductID:         "PAY_UNION",
		HuifuID:           "6666000200643505",
		PrivateKey:        privatePEM,
		PlatformPublicKey: publicPEM,
	}, "DJP20260531140037562501")
	if err != nil {
		t.Fatalf("QueryPayment failed: %v", err)
	}
	data, ok := payload["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data should be an object, got %T", payload["data"])
	}
	if data["org_req_date"] != "20260531" {
		t.Fatalf("org_req_date = %v", data["org_req_date"])
	}
	if data["org_req_seq_id"] != "DJP20260531140037562501" {
		t.Fatalf("org_req_seq_id = %v", data["org_req_seq_id"])
	}
	if data["huifu_id"] != "6666000200643505" {
		t.Fatalf("huifu_id = %v", data["huifu_id"])
	}
	if result.ReqSeqID != "DJP20260531140037562501" || result.HfSeqID != "HFQ123" || result.Status != TransStatusSuccess {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func testRSAKeyPair(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	publicBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey failed: %v", err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicBytes})
	return string(privatePEM), string(publicPEM)
}

func TestVerifyRejectsInvalidBase64Signature(t *testing.T) {
	_, publicPEM := testRSAKeyPair(t)
	if err := Verify([]byte(`{}`), base64.StdEncoding.EncodeToString([]byte("bad")), publicPEM); err == nil {
		t.Fatalf("Verify should reject invalid signature")
	}
}
