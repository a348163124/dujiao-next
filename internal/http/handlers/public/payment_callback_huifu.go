package public

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/http/handlers/shared"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/payment/huifu"

	"github.com/gin-gonic/gin"
)

func (h *Handler) HandleHuifuCallback(c *gin.Context) bool {
	log := shared.RequestLog(c)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return false
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

	trimmed := bytes.TrimSpace(body)
	var form map[string][]string
	if len(trimmed) == 0 {
		parsed, parseErr := parseCallbackForm(c)
		if parseErr != nil {
			log.Debugw("huifu_callback_form_parse_failed", "error", parseErr)
			return false
		}
		form = parsed
		trimmed, err = huifu.FormCallbackBody(form)
		if err != nil {
			log.Debugw("huifu_callback_not_matched", "error", err)
			return false
		}
	} else if trimmed[0] != '{' {
		parsed, parseErr := parseCallbackForm(c)
		if parseErr != nil {
			log.Debugw("huifu_callback_form_parse_failed", "error", parseErr)
			return false
		}
		form = parsed
		trimmed, err = huifu.FormCallbackBody(form)
		if err != nil {
			log.Debugw("huifu_callback_not_matched", "error", err)
			return false
		}
	}

	probe, err := parseHuifuCallbackProbe(trimmed)
	if err != nil || strings.TrimSpace(probe.Sign) == "" || strings.TrimSpace(probe.ReqSeqID) == "" {
		log.Debugw("huifu_callback_not_matched", "error", err)
		return false
	}

	log.Infow("huifu_callback_received",
		"req_seq_id", probe.ReqSeqID,
		"hf_seq_id", probe.HfSeqID,
		"raw_body", callbackRawBodyForLog(body),
	)

	payment, err := h.PaymentRepo.GetByGatewayOrderNo(probe.ReqSeqID)
	if err != nil || payment == nil {
		payment, err = h.PaymentRepo.GetLatestByProviderRef(probe.HfSeqID)
		if err != nil || payment == nil {
			log.Warnw("huifu_callback_payment_not_found", "req_seq_id", probe.ReqSeqID, "hf_seq_id", probe.HfSeqID, "error", err)
			c.String(200, constants.HuifuCallbackFail)
			return true
		}
	}

	channel, err := h.PaymentChannelRepo.GetByID(payment.ChannelID)
	if err != nil || channel == nil {
		log.Warnw("huifu_callback_channel_not_found", "payment_id", payment.ID, "channel_id", payment.ChannelID, "error", err)
		c.String(200, constants.HuifuCallbackFail)
		return true
	}
	if strings.ToLower(strings.TrimSpace(channel.ProviderType)) != constants.PaymentProviderHuifu {
		log.Warnw("huifu_callback_provider_invalid", "provider_type", channel.ProviderType)
		c.String(200, constants.HuifuCallbackFail)
		return true
	}

	updated, err := h.PaymentService.HandleSyncCallback(channel, form, body)
	if err != nil {
		log.Errorw("huifu_callback_handle_failed", "payment_id", payment.ID, "error", err)
		h.enqueuePaymentExceptionAlert(c, models.JSON{
			"alert_type":  "huifu_callback_handle_failed",
			"alert_level": "error",
			"payment_id":  fmt.Sprintf("%d", payment.ID),
			"req_seq_id":  probe.ReqSeqID,
			"provider":    constants.PaymentProviderHuifu,
			"message":     strings.TrimSpace(err.Error()),
		})
		c.String(200, constants.HuifuCallbackFail)
		return true
	}

	log.Infow("huifu_callback_processed", "payment_id", payment.ID, "status", updated.Status)
	c.String(200, constants.HuifuCallbackSuccess)
	return true
}

type huifuCallbackProbe struct {
	Sign     string
	ReqSeqID string
	HfSeqID  string
}

func parseHuifuCallbackProbe(body []byte) (*huifuCallbackProbe, error) {
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(body, &outer); err != nil {
		return nil, err
	}
	probe := &huifuCallbackProbe{Sign: strings.TrimSpace(rawHuifuProbeString(outer, "sign", "SIGN"))}
	dataRaw := bytes.TrimSpace(rawHuifuProbeMessage(outer, "data", "DATA", "resp_data", "respData", "RESP_DATA"))
	if len(dataRaw) > 0 {
		if dataRaw[0] == '"' {
			var text string
			if err := json.Unmarshal(dataRaw, &text); err != nil {
				return nil, err
			}
			dataRaw = []byte(strings.TrimSpace(text))
		}
		var data map[string]interface{}
		if err := json.Unmarshal(dataRaw, &data); err != nil {
			return nil, err
		}
		probe.ReqSeqID = pickHuifuProbeString(data, "req_seq_id", "reqSeqId", "org_req_seq_id", "orgReqSeqId")
		probe.HfSeqID = pickHuifuProbeString(data, "hf_seq_id", "hfSeqId", "org_hf_seq_id", "orgHfSeqId")
		return probe, nil
	}

	var flat map[string]interface{}
	if err := json.Unmarshal(body, &flat); err != nil {
		return nil, err
	}
	probe.ReqSeqID = pickHuifuProbeString(flat, "req_seq_id", "reqSeqId", "org_req_seq_id", "orgReqSeqId")
	probe.HfSeqID = pickHuifuProbeString(flat, "hf_seq_id", "hfSeqId", "org_hf_seq_id", "orgHfSeqId")
	return probe, nil
}

func rawHuifuProbeMessage(payload map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if raw, ok := payload[key]; ok && len(bytes.TrimSpace(raw)) > 0 {
			return raw
		}
	}
	return nil
}

func rawHuifuProbeString(payload map[string]json.RawMessage, keys ...string) string {
	raw := rawHuifuProbeMessage(payload, keys...)
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return strings.Trim(strings.TrimSpace(string(raw)), "\"")
}

func pickHuifuProbeString(data map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := data[key]; ok && value != nil {
			switch v := value.(type) {
			case string:
				if s := strings.TrimSpace(v); s != "" {
					return s
				}
			default:
				b, _ := json.Marshal(v)
				if s := strings.Trim(strings.TrimSpace(string(b)), "\""); s != "" && s != "null" {
					return s
				}
			}
		}
	}
	return ""
}
