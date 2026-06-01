package huifu

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/payment/common"
)

const (
	defaultGatewayURL   = "https://api.huifu.com"
	preorderPath        = "/v2/trade/hosting/payment/preorder"
	queryPath           = "/v2/trade/hosting/payment/queryorderinfo"
	defaultProductID    = "PAY_UNION"
	defaultCurrency     = constants.SiteCurrencyDefault
	defaultPreOrderType = "1"
	timeExpireLayout    = "20060102150405"

	TransStatusSuccess = "S"
	TransStatusFailed  = "F"
	TransStatusClosed  = "C"
)

var (
	ErrConfigInvalid    = errors.New("huifu config invalid")
	ErrRequestFailed    = errors.New("huifu request failed")
	ErrResponseInvalid  = errors.New("huifu response invalid")
	ErrSignatureInvalid = errors.New("huifu signature invalid")
)

type Config struct {
	GatewayURL        string `json:"gateway_url"`
	SysID             string `json:"sys_id"`
	ProductID         string `json:"product_id"`
	HuifuID           string `json:"huifu_id"`
	AcctID            string `json:"acct_id"`
	PrivateKey        string `json:"private_key"`
	PlatformPublicKey string `json:"platform_public_key"`
	NotifyURL         string `json:"notify_url"`
	ReturnURL         string `json:"return_url"`
	TransType         string `json:"trans_type"`
	UsageType         string `json:"usage_type"`
	PreOrderType      string `json:"pre_order_type"`
	HostingOrderID    string `json:"hosting_order_id"`
	TimeExpire        string `json:"time_expire"`
	DelayAcctFlag     string `json:"delay_acct_flag"`
	MultiPayWayFlag   string `json:"multi_pay_way_flag"`
	AcctSplitBunch    string `json:"acct_split_bunch"`
	ProjectTitle      string `json:"project_title"`
	ProjectID         string `json:"project_id"`
	CallbackURL       string `json:"callback_url"`
	HostingData       string `json:"hosting_data"`
	BizInfo           string `json:"biz_info"`
	FeeSign           string `json:"fee_sign"`
	TargetCurrency    string `json:"target_currency"`
	ExchangeRate      string `json:"exchange_rate"`
}

type CreateInput struct {
	ReqSeqID       string
	HostingOrderID string
	Amount         string
	GoodsDesc      string
	NotifyURL      string
	ReturnURL      string
	ClientIP       string
	Currency       string
	TimeExpire     string
	HostingData    map[string]interface{}
}

type CreateResult struct {
	ReqSeqID    string
	HfSeqID     string
	PartyOrder  string
	Status      string
	JumpURL     string
	TransAmount string
	Raw         map[string]interface{}
}

type QueryResult struct {
	ReqSeqID    string
	HfSeqID     string
	PartyOrder  string
	Status      string
	TransAmount string
	TransTime   string
	Raw         map[string]interface{}
}

type CallbackData struct {
	Raw         map[string]interface{}
	ReqSeqID    string
	HfSeqID     string
	PartyOrder  string
	TransStatus string
	TransAmount string
	FeeAmount   string
	PayType     string
	TransTime   string
	AcctDate    string
}

func ParseConfig(raw map[string]interface{}) (*Config, error) {
	cfg, err := common.ParseConfig[Config](raw, ErrConfigInvalid)
	if err != nil {
		return nil, err
	}
	cfg.Normalize()
	return cfg, nil
}

func (c *Config) Normalize() {
	c.GatewayURL = strings.TrimRight(strings.TrimSpace(c.GatewayURL), "/")
	if c.GatewayURL == "" {
		c.GatewayURL = defaultGatewayURL
	}
	c.SysID = strings.TrimSpace(c.SysID)
	c.ProductID = strings.TrimSpace(c.ProductID)
	if c.ProductID == "" {
		c.ProductID = defaultProductID
	}
	c.HuifuID = strings.TrimSpace(c.HuifuID)
	c.AcctID = strings.TrimSpace(c.AcctID)
	c.PrivateKey = strings.TrimSpace(c.PrivateKey)
	c.PlatformPublicKey = strings.TrimSpace(c.PlatformPublicKey)
	c.NotifyURL = strings.TrimSpace(c.NotifyURL)
	c.ReturnURL = strings.TrimSpace(c.ReturnURL)
	c.TransType = strings.TrimSpace(c.TransType)
	c.UsageType = strings.TrimSpace(c.UsageType)
	c.PreOrderType = strings.TrimSpace(c.PreOrderType)
	if c.PreOrderType == "" {
		c.PreOrderType = defaultPreOrderType
	}
	c.HostingOrderID = strings.TrimSpace(c.HostingOrderID)
	c.TimeExpire = strings.TrimSpace(c.TimeExpire)
	c.DelayAcctFlag = strings.TrimSpace(c.DelayAcctFlag)
	if c.DelayAcctFlag == "" {
		c.DelayAcctFlag = "N"
	}
	c.MultiPayWayFlag = strings.TrimSpace(c.MultiPayWayFlag)
	c.AcctSplitBunch = strings.TrimSpace(c.AcctSplitBunch)
	c.ProjectTitle = strings.TrimSpace(c.ProjectTitle)
	c.ProjectID = strings.TrimSpace(c.ProjectID)
	c.CallbackURL = strings.TrimSpace(c.CallbackURL)
	c.HostingData = strings.TrimSpace(c.HostingData)
	c.BizInfo = strings.TrimSpace(c.BizInfo)
	c.FeeSign = strings.TrimSpace(c.FeeSign)
	c.TargetCurrency = strings.ToUpper(strings.TrimSpace(c.TargetCurrency))
	c.ExchangeRate = strings.TrimSpace(c.ExchangeRate)
}

func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("%w: config is nil", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.GatewayURL) == "" {
		return fmt.Errorf("%w: gateway_url is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.SysID) == "" {
		return fmt.Errorf("%w: sys_id is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.ProductID) == "" {
		return fmt.Errorf("%w: product_id is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.HuifuID) == "" {
		return fmt.Errorf("%w: huifu_id is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.PrivateKey) == "" {
		return fmt.Errorf("%w: private_key is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.PlatformPublicKey) == "" {
		return fmt.Errorf("%w: platform_public_key is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.NotifyURL) == "" {
		return fmt.Errorf("%w: notify_url is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.ReturnURL) == "" {
		return fmt.Errorf("%w: return_url is required", ErrConfigInvalid)
	}
	if _, err := parsePrivateKey(cfg.PrivateKey); err != nil {
		return fmt.Errorf("%w: invalid private_key", ErrConfigInvalid)
	}
	if _, err := parsePublicKey(cfg.PlatformPublicKey); err != nil {
		return fmt.Errorf("%w: invalid platform_public_key", ErrConfigInvalid)
	}
	return nil
}

func CreatePayment(ctx context.Context, cfg *Config, input CreateInput) (*CreateResult, error) {
	if cfg == nil {
		return nil, ErrConfigInvalid
	}
	cfg.Normalize()
	if strings.TrimSpace(input.ReqSeqID) == "" || strings.TrimSpace(input.Amount) == "" || strings.TrimSpace(input.GoodsDesc) == "" {
		return nil, ErrConfigInvalid
	}
	notifyURL := strings.TrimSpace(input.NotifyURL)
	if notifyURL == "" {
		notifyURL = strings.TrimSpace(cfg.NotifyURL)
	}
	returnURL := strings.TrimSpace(input.ReturnURL)
	if returnURL == "" {
		returnURL = strings.TrimSpace(cfg.ReturnURL)
	}
	if notifyURL == "" || returnURL == "" {
		return nil, fmt.Errorf("%w: notify_url and return_url are required", ErrConfigInvalid)
	}

	hostingOrderID := strings.TrimSpace(input.HostingOrderID)
	if hostingOrderID == "" {
		hostingOrderID = strings.TrimSpace(cfg.HostingOrderID)
	}
	if hostingOrderID == "" {
		hostingOrderID = strings.TrimSpace(input.ReqSeqID)
	}
	timeExpire, err := formatTimeExpire(firstNonEmpty(input.TimeExpire, cfg.TimeExpire), time.Now(), huifuTimeLocation())
	if err != nil {
		return nil, fmt.Errorf("%w: invalid time_expire", ErrConfigInvalid)
	}
	usageType := normalizeUsageType(cfg.UsageType)
	preOrderType := strings.TrimSpace(cfg.PreOrderType)
	if preOrderType == "" {
		preOrderType = defaultPreOrderType
	}

	data := map[string]interface{}{
		"req_date":         time.Now().Format("20060102"),
		"req_seq_id":       strings.TrimSpace(input.ReqSeqID),
		"hosting_order_id": hostingOrderID,
		"huifu_id":         strings.TrimSpace(cfg.HuifuID),
		"trans_amt":        strings.TrimSpace(input.Amount),
		"goods_desc":       strings.TrimSpace(input.GoodsDesc),
		"notify_url":       notifyURL,
	}
	addString(data, "usage_type", usageType)
	addString(data, "acct_id", cfg.AcctID)
	addString(data, "trans_type", cfg.TransType)
	addString(data, "pre_order_type", preOrderType)
	addString(data, "delay_acct_flag", cfg.DelayAcctFlag)
	addString(data, "multi_pay_way_flag", cfg.MultiPayWayFlag)
	addJSONString(data, "acct_split_bunch", cfg.AcctSplitBunch)
	addHostingData(data, cfg, input.HostingData)
	addJSONString(data, "biz_info", cfg.BizInfo)
	addString(data, "fee_sign", cfg.FeeSign)
	addString(data, "time_expire", timeExpire)
	if currency := strings.ToUpper(strings.TrimSpace(input.Currency)); currency != "" && currency != defaultCurrency {
		data["currency"] = currency
	}

	body, err := postSignedData(ctx, cfg, preorderPath, data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}

	result, err := ParseCreateResponse(body, cfg.PlatformPublicKey)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func QueryPayment(ctx context.Context, cfg *Config, reqSeqID string) (*QueryResult, error) {
	if cfg == nil {
		return nil, ErrConfigInvalid
	}
	cfg.Normalize()
	reqSeqID = strings.TrimSpace(reqSeqID)
	if reqSeqID == "" {
		return nil, fmt.Errorf("%w: req_seq_id is required", ErrConfigInvalid)
	}
	now := time.Now().In(huifuTimeLocation())
	data := map[string]interface{}{
		"req_date":       now.Format("20060102"),
		"req_seq_id":     nextHuifuReqSeqID(now),
		"org_req_date":   inferHuifuOrgReqDate(reqSeqID, now),
		"org_req_seq_id": reqSeqID,
		"huifu_id":       strings.TrimSpace(cfg.HuifuID),
	}
	body, err := postSignedData(ctx, cfg, queryPath, data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	return ParseQueryResponse(body, cfg.PlatformPublicKey)
}

func postSignedData(ctx context.Context, cfg *Config, path string, data map[string]interface{}) ([]byte, error) {
	dataBody, err := marshalJSON(data)
	if err != nil {
		return nil, err
	}
	sign, err := Sign(dataBody, cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	payload := map[string]interface{}{
		"sys_id":     strings.TrimSpace(cfg.SysID),
		"product_id": strings.TrimSpace(cfg.ProductID),
		"sign":       sign,
		"data":       data,
	}
	return postJSON(ctx, strings.TrimRight(cfg.GatewayURL, "/")+path, payload)
}

func parseSignedResponseData(body []byte, platformPublicKey string) (map[string]interface{}, error) {
	if len(body) == 0 {
		return nil, ErrResponseInvalid
	}
	var outer map[string]interface{}
	if err := decodeJSONUseNumber(body, &outer); err != nil {
		return nil, fmt.Errorf("%w: decode response failed", ErrResponseInvalid)
	}
	respCode := pickString(outer, "resp_code", "respCode")
	if respCode != "" && respCode != "00000000" {
		return outer, nil
	}
	sign := strings.TrimSpace(pickString(outer, "sign", "SIGN"))
	dataValue, ok := outer["data"]
	if !ok || dataValue == nil {
		if _, ok := outer["resp_code"]; ok {
			return outer, nil
		}
		return nil, fmt.Errorf("%w: data is empty", ErrResponseInvalid)
	}
	dataRaw, err := responseDataRaw(dataValue)
	if err != nil {
		return nil, err
	}
	if sign != "" {
		if err := Verify(dataRaw, sign, platformPublicKey); err != nil {
			return nil, err
		}
	}
	var data map[string]interface{}
	if err := decodeJSONUseNumber(dataRaw, &data); err != nil {
		return nil, fmt.Errorf("%w: decode data failed", ErrResponseInvalid)
	}
	if respCode != "" {
		data["resp_code"] = respCode
	}
	if respDesc := pickString(outer, "resp_desc", "respDesc"); respDesc != "" {
		data["resp_desc"] = respDesc
	}
	return data, nil
}

func responseDataRaw(value interface{}) ([]byte, error) {
	switch v := value.(type) {
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return nil, fmt.Errorf("%w: data is empty", ErrResponseInvalid)
		}
		return []byte(text), nil
	case json.RawMessage:
		return normalizeRawMessage(v), nil
	default:
		dataRaw, err := marshalJSON(v)
		if err != nil {
			return nil, fmt.Errorf("%w: encode data failed", ErrResponseInvalid)
		}
		return dataRaw, nil
	}
}

func nextHuifuReqSeqID(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return fmt.Sprintf("DJQ%s%06d", now.In(huifuTimeLocation()).Format("20060102150405"), now.Nanosecond()/1000)
}

func inferHuifuOrgReqDate(reqSeqID string, fallback time.Time) string {
	digits := make([]byte, 0, len(reqSeqID))
	for i := 0; i < len(reqSeqID); i++ {
		if reqSeqID[i] >= '0' && reqSeqID[i] <= '9' {
			digits = append(digits, reqSeqID[i])
		}
	}
	if len(digits) >= 8 {
		date := string(digits[:8])
		if _, err := time.Parse("20060102", date); err == nil {
			return date
		}
	}
	if fallback.IsZero() {
		fallback = time.Now()
	}
	return fallback.In(huifuTimeLocation()).Format("20060102")
}

func ParseCreateResponse(body []byte, platformPublicKey string) (*CreateResult, error) {
	data, err := parseSignedResponseData(body, platformPublicKey)
	if err != nil {
		return nil, err
	}
	respCode := pickString(data, "resp_code", "respCode")
	if respCode != "" && respCode != "00000000" {
		return nil, fmt.Errorf("%w: %s", ErrResponseInvalid, pickString(data, "resp_desc", "respDesc"))
	}
	result := &CreateResult{
		ReqSeqID:    pickString(data, "req_seq_id", "reqSeqId"),
		HfSeqID:     pickString(data, "hf_seq_id", "hfSeqId"),
		PartyOrder:  pickString(data, "party_order_id", "partyOrderId"),
		Status:      pickString(data, "trans_stat", "transStat", "status"),
		JumpURL:     pickString(data, "jump_url", "jumpUrl"),
		TransAmount: pickString(data, "trans_amt", "transAmt"),
		Raw:         data,
	}
	if result.JumpURL == "" {
		return nil, fmt.Errorf("%w: jump_url is empty", ErrResponseInvalid)
	}
	return result, nil
}

func ParseQueryResponse(body []byte, platformPublicKey string) (*QueryResult, error) {
	data, err := parseSignedResponseData(body, platformPublicKey)
	if err != nil {
		return nil, err
	}
	respCode := pickString(data, "resp_code", "respCode")
	if respCode != "" && respCode != "00000000" {
		return nil, fmt.Errorf("%w: %s", ErrResponseInvalid, pickString(data, "resp_desc", "respDesc"))
	}
	return &QueryResult{
		ReqSeqID:    pickString(data, "req_seq_id", "reqSeqId", "org_req_seq_id", "orgReqSeqId"),
		HfSeqID:     pickString(data, "hf_seq_id", "hfSeqId", "org_hf_seq_id", "orgHfSeqId"),
		PartyOrder:  pickString(data, "party_order_id", "partyOrderId"),
		Status:      pickString(data, "trans_stat", "transStat", "trans_status", "transStatus", "order_stat", "orderStat"),
		TransAmount: pickString(data, "trans_amt", "transAmt"),
		TransTime:   pickString(data, "trans_time", "transTime", "end_time", "endTime"),
		Raw:         data,
	}, nil
}

func ParseCallback(body []byte) (*CallbackData, string, []byte, error) {
	if len(body) == 0 {
		return nil, "", nil, ErrResponseInvalid
	}
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(body, &outer); err == nil {
		sign := strings.TrimSpace(rawCallbackString(outer, "sign", "SIGN"))
		dataRawMessage := rawCallbackMessage(outer, "data", "DATA", "resp_data", "respData", "RESP_DATA")
		if sign != "" && len(dataRawMessage) > 0 {
			dataRaw := normalizeRawMessage(dataRawMessage)
			data, err := parseCallbackDataRaw(dataRaw)
			if err != nil {
				return nil, "", nil, err
			}
			return data, sign, dataRaw, nil
		}
	}
	var payload map[string]interface{}
	if err := decodeJSONUseNumber(body, &payload); err != nil {
		return nil, "", nil, fmt.Errorf("%w: decode callback failed", ErrResponseInvalid)
	}
	sign := strings.TrimSpace(pickString(payload, "sign", "SIGN"))
	if sign == "" {
		return nil, "", nil, fmt.Errorf("%w: sign is empty", ErrResponseInvalid)
	}
	delete(payload, "sign")
	delete(payload, "SIGN")
	dataRaw, err := json.Marshal(payload)
	if err != nil {
		return nil, "", nil, fmt.Errorf("%w: encode callback data failed", ErrResponseInvalid)
	}
	data, err := parseCallbackDataMap(payload)
	if err != nil {
		return nil, "", nil, err
	}
	return data, sign, dataRaw, nil
}

func rawCallbackMessage(payload map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if raw, ok := payload[key]; ok && len(bytes.TrimSpace(raw)) > 0 {
			return raw
		}
	}
	return nil
}

func rawCallbackString(payload map[string]json.RawMessage, keys ...string) string {
	raw := rawCallbackMessage(payload, keys...)
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return strings.Trim(strings.TrimSpace(string(raw)), "\"")
}

func VerifyCallback(cfg *Config, body []byte) (*CallbackData, error) {
	if cfg == nil {
		return nil, ErrConfigInvalid
	}
	data, sign, dataRaw, err := ParseCallback(body)
	if err != nil {
		return nil, err
	}
	if err := Verify(dataRaw, sign, cfg.PlatformPublicKey); err != nil {
		return nil, err
	}
	return data, nil
}

func FormCallbackBody(form map[string][]string) ([]byte, error) {
	if len(form) == 0 {
		return nil, fmt.Errorf("%w: callback form is empty", ErrResponseInvalid)
	}
	sign := strings.TrimSpace(firstFormValue(form, "sign", "SIGN"))
	dataText := strings.TrimSpace(firstFormValue(form, "data", "DATA"))
	if sign == "" {
		return nil, fmt.Errorf("%w: sign is empty", ErrResponseInvalid)
	}
	if dataText != "" {
		var data map[string]interface{}
		if err := decodeJSONUseNumber([]byte(dataText), &data); err != nil {
			return nil, fmt.Errorf("%w: decode callback data failed", ErrResponseInvalid)
		}
		return json.Marshal(map[string]interface{}{
			"sign": sign,
			"data": dataText,
		})
	}

	payload := make(map[string]interface{}, len(form))
	for key, values := range form {
		if len(values) == 0 || strings.EqualFold(key, "sign") {
			continue
		}
		payload[key] = values[0]
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: callback data is empty", ErrResponseInvalid)
	}
	payload["sign"] = sign
	return json.Marshal(payload)
}

func firstFormValue(form map[string][]string, keys ...string) string {
	for _, key := range keys {
		if values, ok := form[key]; ok && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func ToPaymentStatus(transStatus string) string {
	switch strings.ToUpper(strings.TrimSpace(transStatus)) {
	case TransStatusSuccess, "1":
		return constants.PaymentStatusSuccess
	case TransStatusFailed, TransStatusClosed, "5":
		return constants.PaymentStatusFailed
	default:
		return constants.PaymentStatusPending
	}
}

func Sign(content []byte, privateKeyPEM string) (string, error) {
	privateKey, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	signed, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signed), nil
}

func Verify(content []byte, sign string, publicKeyPEM string) error {
	publicKey, err := parsePublicKey(publicKeyPEM)
	if err != nil {
		return fmt.Errorf("%w: invalid platform_public_key", ErrConfigInvalid)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sign))
	if err != nil {
		return ErrSignatureInvalid
	}
	digest := sha256.Sum256(content)
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		return ErrSignatureInvalid
	}
	return nil
}

func parsePrivateKey(raw string) (*rsa.PrivateKey, error) {
	block, err := pemBlock(raw, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not rsa private key")
	}
	return key, nil
}

func parsePublicKey(raw string) (*rsa.PublicKey, error) {
	block, err := pemBlock(raw, "PUBLIC KEY")
	if err != nil {
		return nil, err
	}
	if parsed, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if key, ok := parsed.(*rsa.PublicKey); ok {
			return key, nil
		}
	}
	key, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func pemBlock(raw string, blockType string) (*pem.Block, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, errors.New("empty pem")
	}
	if !strings.Contains(text, "-----BEGIN") {
		text = wrapPEM(text, blockType)
	}
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, errors.New("decode pem failed")
	}
	return block, nil
}

func wrapPEM(body string, blockType string) string {
	body = strings.ReplaceAll(body, "\r", "")
	body = strings.ReplaceAll(body, "\n", "")
	body = strings.ReplaceAll(body, " ", "")
	var b strings.Builder
	b.WriteString("-----BEGIN " + blockType + "-----\n")
	for len(body) > 64 {
		b.WriteString(body[:64])
		b.WriteByte('\n')
		body = body[64:]
	}
	b.WriteString(body)
	b.WriteString("\n-----END " + blockType + "-----")
	return b.String()
}

func parseCallbackDataRaw(dataRaw []byte) (*CallbackData, error) {
	var payload map[string]interface{}
	if err := decodeJSONUseNumber(dataRaw, &payload); err != nil {
		return nil, fmt.Errorf("%w: decode callback data failed", ErrResponseInvalid)
	}
	return parseCallbackDataMap(payload)
}

func parseCallbackDataMap(payload map[string]interface{}) (*CallbackData, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: empty callback data", ErrResponseInvalid)
	}
	return &CallbackData{
		Raw:         payload,
		ReqSeqID:    pickString(payload, "req_seq_id", "reqSeqId", "org_req_seq_id", "orgReqSeqId"),
		HfSeqID:     pickString(payload, "hf_seq_id", "hfSeqId", "org_hf_seq_id", "orgHfSeqId"),
		PartyOrder:  pickString(payload, "party_order_id", "partyOrderId"),
		TransStatus: pickString(payload, "trans_stat", "transStat", "trans_status", "transStatus", "order_stat", "orderStat"),
		TransAmount: pickString(payload, "trans_amt", "transAmt"),
		FeeAmount:   pickString(payload, "fee_amount", "feeAmount"),
		PayType:     pickString(payload, "pay_type", "payType"),
		TransTime:   pickString(payload, "trans_time", "transTime", "end_time", "endTime"),
		AcctDate:    pickString(payload, "acct_date", "acctDate"),
	}, nil
}

func ParsePaidAt(raw string) *time.Time {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}
	layouts := []string{
		"20060102150405",
		"2006-01-02 15:04:05",
		time.RFC3339,
	}
	for _, layout := range layouts {
		parsed, err := time.ParseInLocation(layout, text, time.Local)
		if err == nil {
			return &parsed
		}
	}
	return nil
}

func normalizeRawMessage(raw json.RawMessage) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] != '"' {
		return trimmed
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return trimmed
	}
	return []byte(strings.TrimSpace(text))
}

func addString(data map[string]interface{}, key string, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		data[key] = value
	}
}

func addJSONString(data map[string]interface{}, key string, raw string) {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		data[key] = raw
	}
}

func addHostingData(data map[string]interface{}, cfg *Config, dynamic map[string]interface{}) {
	hostingData := map[string]interface{}{}
	if cfg != nil {
		if raw := strings.TrimSpace(cfg.HostingData); raw != "" {
			var value map[string]interface{}
			if err := json.Unmarshal([]byte(raw), &value); err == nil {
				for key, val := range value {
					hostingData[key] = val
				}
			}
		}
		addHostingDataString(hostingData, "project_title", cfg.ProjectTitle)
		addHostingDataString(hostingData, "project_id", cfg.ProjectID)
		addHostingDataString(hostingData, "callback_url", cfg.CallbackURL)
	}
	for key, val := range dynamic {
		if text, ok := val.(string); ok {
			if strings.TrimSpace(text) == "" {
				continue
			}
			hostingData[key] = strings.TrimSpace(text)
			continue
		}
		if val != nil {
			hostingData[key] = val
		}
	}
	if len(hostingData) > 0 {
		data["hosting_data"] = string(mustMarshalJSON(hostingData))
	}
}

func addHostingDataString(hostingData map[string]interface{}, key string, value string) {
	if value = strings.TrimSpace(value); value != "" {
		hostingData[key] = value
	}
}

func normalizeUsageType(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "P", "R":
		return strings.ToUpper(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func formatTimeExpire(raw string, now time.Time, loc *time.Location) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if loc == nil {
		loc = time.Local
	}
	if len(raw) == len(timeExpireLayout) {
		if _, err := time.ParseInLocation(timeExpireLayout, raw, loc); err != nil {
			return "", err
		}
		return raw, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return "", fmt.Errorf("invalid duration")
	}
	return now.In(loc).Add(time.Duration(seconds) * time.Second).Format(timeExpireLayout), nil
}

func huifuTimeLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func postJSON(ctx context.Context, endpoint string, payload interface{}) ([]byte, error) {
	body, err := marshalJSON(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

func decodeJSONUseNumber(body []byte, v interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	return decoder.Decode(v)
}

func marshalJSON(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func mustMarshalJSON(v interface{}) []byte {
	b, err := marshalJSON(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func pickString(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := m[key]
		if !ok || value == nil {
			continue
		}
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case json.Number:
			return strings.TrimSpace(v.String())
		case fmt.Stringer:
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		default:
			b, _ := json.Marshal(v)
			if s := strings.Trim(strings.TrimSpace(string(b)), "\""); s != "" && s != "null" {
				return s
			}
		}
	}
	return ""
}
