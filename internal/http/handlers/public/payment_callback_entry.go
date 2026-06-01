package public

import (
	"net/http"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/http/handlers/shared"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/service"

	"github.com/gin-gonic/gin"
)

func (h *Handler) PaymentCallback(c *gin.Context) {
	shared.RequestLog(c).Infow("payment_callback_received",
		"method", c.Request.Method,
		"client_ip", c.ClientIP(),
		"content_type", strings.TrimSpace(c.GetHeader("Content-Type")),
	)
	if handled := h.HandleWechatCallback(c); handled {
		return
	}
	if handled := h.HandleOkpayCallback(c); handled {
		return
	}
	if handled := h.HandleAlipayCallback(c); handled {
		return
	}
	if handled := h.HandleEpayCallback(c); handled {
		return
	}
	if handled := h.HandleTokenPayCallback(c); handled {
		return
	}
	if handled := h.HandleEpusdtCallback(c); handled {
		return
	}
	if handled := h.HandleBepusdtCallback(c); handled {
		return
	}
	if handled := h.HandleHuifuCallback(c); handled {
		return
	}
	shared.RequestLog(c).Warnw("payment_callback_unrecognized",
		"method", c.Request.Method,
		"client_ip", c.ClientIP(),
		"content_type", strings.TrimSpace(c.GetHeader("Content-Type")),
	)
	h.enqueuePaymentExceptionAlert(c, models.JSON{
		"alert_type":  "callback_unrecognized",
		"alert_level": "warning",
		"message":     "支付回调请求无法匹配已支持的回调格式",
	})
	c.AbortWithStatus(http.StatusNotFound)
	return
}

func parseCallbackForm(c *gin.Context) (map[string][]string, error) {
	normalizeCallbackRequestQuery(c.Request)
	if err := c.Request.ParseForm(); err != nil {
		return nil, err
	}
	if len(c.Request.PostForm) > 0 {
		return c.Request.PostForm, nil
	}
	return c.Request.Form, nil
}

// normalizeCallbackRequestQuery 兼容部分网关或中间层产生的非标准 query 格式：
//  1. 将 HTML 转义的 &amp; 还原为 &（否则会被解析成名为 amp; 开头的参数）
//  2. 将以 ; 分隔的参数兼容转换为 & 分隔（Go 1.17+ 的 url.ParseQuery 不再接受 ; 作为分隔符，
//     否则 ParseForm 会返回 "invalid semicolon separator in query" 并导致回调无法识别）
func normalizeCallbackRequestQuery(r *http.Request) {
	if r == nil || r.URL == nil {
		return
	}
	raw := r.URL.RawQuery
	if raw == "" {
		return
	}
	normalized := raw
	if strings.Contains(normalized, "&amp;") {
		normalized = strings.ReplaceAll(normalized, "&amp;", "&")
	}
	if strings.Contains(normalized, ";") {
		normalized = strings.ReplaceAll(normalized, ";", "&")
	}
	if normalized != raw {
		r.URL.RawQuery = normalized
	}
}

func getFirstValue(form map[string][]string, key string) string {
	if values, ok := form[key]; ok && len(values) > 0 {
		return values[0]
	}
	return ""
}

func truncateCallbackLogValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if len(raw) <= callbackLogValueLimit {
		return raw
	}
	return raw[:callbackLogValueLimit] + "...(truncated)"
}

func callbackRawBodyForLog(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return truncateCallbackLogValue(string(body))
}

func callbackRawFormForLog(form map[string][]string) map[string]interface{} {
	result := make(map[string]interface{}, len(form))
	for key, values := range form {
		if len(values) == 0 {
			result[key] = ""
			continue
		}
		if len(values) == 1 {
			result[key] = truncateCallbackLogValue(values[0])
			continue
		}
		copied := make([]string, 0, len(values))
		for _, value := range values {
			copied = append(copied, truncateCallbackLogValue(value))
		}
		result[key] = copied
	}
	return result
}

func (h *Handler) enqueuePaymentExceptionAlert(c *gin.Context, data models.JSON) {
	if h == nil || h.Container == nil || h.NotificationService == nil {
		return
	}
	payload := models.JSON{
		"source":      constants.NotificationBizTypePaymentCallback,
		"method":      strings.TrimSpace(c.Request.Method),
		"path":        strings.TrimSpace(c.Request.URL.Path),
		"client_ip":   strings.TrimSpace(c.ClientIP()),
		"occurred_at": time.Now().Format("2006-01-02 15:04:05"),
	}
	for key, value := range data {
		payload[key] = value
	}
	if err := h.NotificationService.Enqueue(service.NotificationEnqueueInput{
		EventType: constants.NotificationEventExceptionAlert,
		BizType:   constants.NotificationBizTypePaymentCallback,
		BizID:     0,
		Data:      payload,
	}); err != nil {
		shared.RequestLog(c).Warnw("enqueue_payment_exception_alert_failed", "error", err)
	}
}
