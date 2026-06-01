package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/payment/huifu"

	"github.com/shopspring/decimal"
)

type huifuAdapter struct{}

func NewHuifuAdapter() Provider { return &huifuAdapter{} }

var (
	_ Provider         = (*huifuAdapter)(nil)
	_ Capturer         = (*huifuAdapter)(nil)
	_ CallbackVerifier = (*huifuAdapter)(nil)
)

func (a *huifuAdapter) Type() string {
	return constants.PaymentProviderHuifu + ":"
}

func (a *huifuAdapter) parseConfig(raw models.JSON) (*huifu.Config, error) {
	cfg, err := huifu.ParseConfig(raw)
	if err != nil {
		return nil, mapHuifuError(err)
	}
	if err := huifu.ValidateConfig(cfg); err != nil {
		return nil, mapHuifuError(err)
	}
	return cfg, nil
}

func (a *huifuAdapter) ValidateConfig(raw models.JSON, interactionMode string) error {
	mode := strings.ToLower(strings.TrimSpace(interactionMode))
	if mode != "" && mode != constants.PaymentInteractionRedirect {
		return fmt.Errorf("%w: huifu only supports redirect interaction_mode", ErrUnsupportedChannel)
	}
	_, err := a.parseConfig(raw)
	return err
}

func (a *huifuAdapter) CreatePayment(ctx context.Context, raw models.JSON, input CreateInput) (*CreateResult, error) {
	if input.ChannelType != "" && !isHuifuSupportedChannelType(input.ChannelType) {
		return nil, fmt.Errorf("%w: huifu channel_type %s", ErrUnsupportedChannel, input.ChannelType)
	}
	mode, _ := input.Extra["interaction_mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "" && mode != constants.PaymentInteractionRedirect {
		return nil, fmt.Errorf("%w: huifu interaction_mode %s", ErrConfigInvalid, mode)
	}

	cfg, err := a.parseConfig(raw)
	if err != nil {
		return nil, err
	}

	notifyURL := strings.TrimSpace(input.NotifyURL)
	if notifyURL == "" {
		notifyURL = strings.TrimSpace(cfg.NotifyURL)
	}
	returnURL := strings.TrimSpace(input.ReturnURL)
	if returnURL == "" {
		returnURL = strings.TrimSpace(cfg.ReturnURL)
	}
	returnURL = appendQueryParams(returnURL, input.ReturnURLQuery)

	originalAmount := input.Amount.Decimal.String()
	originalCurrency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if originalCurrency == "" {
		originalCurrency = constants.SiteCurrencyDefault
	}
	payAmount := input.Amount.Decimal.Round(2).StringFixed(2)
	payCurrency := originalCurrency
	converted := false
	if needsHuifuCurrencyConversion(cfg, originalCurrency) {
		convertedAmount, convertedCurrency, convErr := convertHuifuAmount(payAmount, originalCurrency, cfg)
		if convErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrConfigInvalid, convErr)
		}
		payAmount = convertedAmount
		payCurrency = convertedCurrency
		converted = true
	}

	hostingData := buildHuifuHostingData(cfg, returnURL)
	logger.Infow("huifu_preorder_request",
		"req_seq_id", input.OrderNo,
		"huifu_id", cfg.HuifuID,
		"product_id", cfg.ProductID,
		"trans_amt", payAmount,
		"goods_desc", input.Subject,
		"pre_order_type", cfg.PreOrderType,
		"time_expire", cfg.TimeExpire,
		"hosting_data", hostingData,
	)
	result, err := huifu.CreatePayment(ctx, cfg, huifu.CreateInput{
		ReqSeqID:       input.OrderNo,
		HostingOrderID: input.OrderNo,
		Amount:         payAmount,
		GoodsDesc:      input.Subject,
		NotifyURL:      notifyURL,
		ReturnURL:      returnURL,
		ClientIP:       input.ClientIP,
		Currency:       payCurrency,
		HostingData:    hostingData,
	})
	if err != nil {
		return nil, mapHuifuError(err)
	}

	payload := models.JSON{}
	if result.Raw != nil {
		payload = models.JSON(result.Raw)
	}
	if converted {
		payload["exchange_rate"] = strings.TrimSpace(cfg.ExchangeRate)
		payload["original_amount"] = originalAmount
		payload["original_currency"] = originalCurrency
	}

	return &CreateResult{
		ProviderRef:  pickFirstNonEmpty(result.HfSeqID, result.PartyOrder, result.ReqSeqID),
		RedirectURL:  result.JumpURL,
		Payload:      payload,
		AmountSent:   payAmount,
		CurrencySent: payCurrency,
	}, nil
}

func (a *huifuAdapter) QueryPayment(ctx context.Context, raw models.JSON, providerRef string) (*QueryResult, error) {
	cfg, err := a.parseConfig(raw)
	if err != nil {
		return nil, err
	}
	result, err := huifu.QueryPayment(ctx, cfg, providerRef)
	if err != nil {
		return nil, mapHuifuError(err)
	}

	amount := models.Money{}
	if s := strings.TrimSpace(result.TransAmount); s != "" {
		if d, parseErr := decimal.NewFromString(s); parseErr == nil {
			amount = models.NewMoneyFromDecimal(d)
		}
	}
	payload := models.JSON{}
	if result.Raw != nil {
		payload = models.JSON(result.Raw)
	}
	return &QueryResult{
		ProviderRef: pickFirstNonEmpty(result.HfSeqID, result.PartyOrder),
		Status:      huifu.ToPaymentStatus(result.Status),
		Amount:      amount,
		Currency:    constants.SiteCurrencyDefault,
		PaidAt:      huifu.ParsePaidAt(result.TransTime),
		Payload:     payload,
	}, nil
}

func (a *huifuAdapter) VerifyCallback(raw models.JSON, form map[string][]string, body []byte) (*CallbackResult, error) {
	cfg, err := huifu.ParseConfig(raw)
	if err != nil {
		return nil, mapHuifuError(err)
	}
	callbackBody := body
	if len(form) > 0 {
		callbackBody, err = huifu.FormCallbackBody(form)
		if err != nil {
			return nil, mapHuifuError(err)
		}
	}
	data, err := huifu.VerifyCallback(cfg, callbackBody)
	if err != nil {
		return nil, mapHuifuError(err)
	}

	amount := models.Money{}
	if s := strings.TrimSpace(data.TransAmount); s != "" {
		if d, parseErr := decimal.NewFromString(s); parseErr == nil {
			amount = models.NewMoneyFromDecimal(d)
		}
	}

	payload := models.JSON{}
	if data.Raw != nil {
		payload = models.JSON(data.Raw)
	} else if pb, marshalErr := json.Marshal(data); marshalErr == nil {
		var m map[string]interface{}
		if jsonErr := json.Unmarshal(pb, &m); jsonErr == nil {
			payload = models.JSON(m)
		}
	}

	return &CallbackResult{
		OrderNo:     data.ReqSeqID,
		ProviderRef: pickFirstNonEmpty(data.HfSeqID, data.PartyOrder),
		Status:      huifu.ToPaymentStatus(data.TransStatus),
		Amount:      amount,
		Currency:    constants.SiteCurrencyDefault,
		PaidAt:      huifu.ParsePaidAt(data.TransTime),
		Payload:     payload,
	}, nil
}

func buildHuifuHostingData(cfg *huifu.Config, returnURL string) map[string]interface{} {
	hostingData := map[string]interface{}{}
	if cfg == nil {
		return hostingData
	}
	if title := strings.TrimSpace(cfg.ProjectTitle); title != "" {
		hostingData["project_title"] = title
	}
	if projectID := strings.TrimSpace(cfg.ProjectID); projectID != "" {
		hostingData["project_id"] = projectID
	}
	callbackURL := strings.TrimSpace(returnURL)
	if callbackURL == "" {
		callbackURL = strings.TrimSpace(cfg.CallbackURL)
	}
	if callbackURL != "" {
		hostingData["callback_url"] = callbackURL
	}
	return hostingData
}

func isHuifuSupportedChannelType(channelType string) bool {
	switch strings.ToLower(strings.TrimSpace(channelType)) {
	case "", constants.PaymentChannelTypeCashier, constants.PaymentChannelTypeAlipay, constants.PaymentChannelTypeWechat, constants.PaymentChannelTypeWxpay:
		return true
	default:
		return false
	}
}

func needsHuifuCurrencyConversion(cfg *huifu.Config, originalCurrency string) bool {
	if cfg == nil {
		return false
	}
	return strings.TrimSpace(cfg.ExchangeRate) != "" && strings.TrimSpace(cfg.ExchangeRate) != "1" && strings.TrimSpace(cfg.ExchangeRate) != "1.0" && strings.TrimSpace(cfg.TargetCurrency) != "" && !strings.EqualFold(cfg.TargetCurrency, originalCurrency)
}

func convertHuifuAmount(amount string, currency string, cfg *huifu.Config) (string, string, error) {
	rate, err := decimal.NewFromString(strings.TrimSpace(cfg.ExchangeRate))
	if err != nil || rate.LessThanOrEqual(decimal.Zero) {
		return "", "", fmt.Errorf("invalid exchange_rate")
	}
	amountDec, err := decimal.NewFromString(amount)
	if err != nil {
		return "", "", fmt.Errorf("invalid amount")
	}
	converted := amountDec.Mul(rate).Round(2)
	return converted.StringFixed(2), strings.ToUpper(strings.TrimSpace(cfg.TargetCurrency)), nil
}

func mapHuifuError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, huifu.ErrConfigInvalid):
		return fmt.Errorf("%w: %v", ErrConfigInvalid, err)
	case errors.Is(err, huifu.ErrRequestFailed):
		return fmt.Errorf("%w: %v", ErrRequestFailed, err)
	case errors.Is(err, huifu.ErrResponseInvalid):
		return fmt.Errorf("%w: %v", ErrResponseInvalid, err)
	case errors.Is(err, huifu.ErrSignatureInvalid):
		return fmt.Errorf("%w: %v", ErrSignatureInvalid, err)
	default:
		return err
	}
}
