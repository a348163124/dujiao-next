package public

import (
	"errors"
	"strings"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/dto"
	"github.com/dujiao-next/internal/http/handlers/shared"
	"github.com/dujiao-next/internal/http/response"
	"github.com/dujiao-next/internal/logger"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// enrichOrderWithAllowedChannels 为订单详情附加允许的支付渠道ID
func (h *Handler) enrichOrderWithAllowedChannels(order *models.Order, detail *dto.OrderDetail) {
	if h.PaymentService == nil || order == nil {
		return
	}
	allItems := order.Items
	for _, child := range order.Children {
		allItems = append(allItems, child.Items...)
	}
	allowed := h.PaymentService.GetAllowedChannelIDsForOrder(allItems)
	if len(allowed) > 0 {
		detail.AllowedPaymentChannelIDs = allowed
	}
}

// collectRefundRelevantOrderIDs 收集订单详情应展示退款记录的订单ID（父订单+子订单）。
func collectRefundRelevantOrderIDs(order *models.Order) []uint {
	if order == nil || order.ID == 0 {
		return nil
	}
	seen := map[uint]struct{}{}
	ids := make([]uint, 0, 1+len(order.Children))
	appendID := func(id uint) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	appendID(order.ID)
	for _, child := range order.Children {
		appendID(child.ID)
	}
	return ids
}

// enrichOrderWithRefundRecords 为退款态订单详情补充退款记录列表。
func (h *Handler) enrichOrderWithRefundRecords(order *models.Order, detail *dto.OrderDetail) {
	if h == nil || order == nil || detail == nil || h.OrderRefundRecordRepo == nil {
		return
	}
	status := strings.ToLower(strings.TrimSpace(detail.Status))
	if status != constants.OrderStatusRefunded && status != constants.OrderStatusPartiallyRefunded {
		return
	}

	orderIDs := collectRefundRelevantOrderIDs(order)
	if len(orderIDs) == 0 {
		return
	}
	records, err := h.OrderRefundRecordRepo.ListByOrderIDs(orderIDs)
	if err != nil {
		logger.Warnw("public_order_refund_records_fetch_failed",
			"order_id", order.ID,
			"order_no", order.OrderNo,
			"error", err,
		)
		return
	}
	if len(records) == 0 {
		return
	}

	detail.RefundRecords = make([]dto.OrderRefundResp, 0, len(records))
	for _, record := range records {
		detail.RefundRecords = append(detail.RefundRecords, dto.OrderRefundResp{
			Type:      strings.TrimSpace(record.Type),
			Amount:    record.Amount,
			Currency:  strings.TrimSpace(record.Currency),
			Remark:    strings.TrimSpace(record.Remark),
			CreatedAt: record.CreatedAt,
		})
	}
}

// OrderItemRequest 订单项请求
type OrderItemRequest struct {
	ProductID       uint   `json:"product_id" binding:"required"`
	SKUID           uint   `json:"sku_id"`
	Quantity        int    `json:"quantity" binding:"required"`
	FulfillmentType string `json:"fulfillment_type"`
}

// CreateOrderRequest 创建订单请求
type CreateOrderRequest struct {
	Items               []OrderItemRequest     `json:"items" binding:"required"`
	CouponCode          string                 `json:"coupon_code"`
	AffiliateCode       string                 `json:"affiliate_code"`
	AffiliateVisitorKey string                 `json:"affiliate_visitor_key"`
	ManualFormData      map[string]models.JSON `json:"manual_form_data"`
}

// OrderPaymentChannelsRequest 查询订单可用支付渠道请求
type OrderPaymentChannelsRequest struct {
	Amount  string             `json:"amount" binding:"required"`
	OrderNo string             `json:"order_no"`
	Items   []OrderItemRequest `json:"items"`
}

// PreviewOrder 订单金额预览
func (h *Handler) PreviewOrder(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}

	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	tenant := tenantFromRequest(c)

	var items []service.CreateOrderItem
	for _, item := range req.Items {
		items = append(items, service.CreateOrderItem{
			ProductID:       item.ProductID,
			SKUID:           item.SKUID,
			Quantity:        item.Quantity,
			FulfillmentType: item.FulfillmentType,
		})
	}

	preview, err := h.OrderService.PreviewOrder(service.CreateOrderInput{
		UserID:              uid,
		Tenant:              tenant,
		Items:               items,
		CouponCode:          req.CouponCode,
		AffiliateCode:       req.AffiliateCode,
		AffiliateVisitorKey: req.AffiliateVisitorKey,
		ClientIP:            c.ClientIP(),
		ManualFormData:      req.ManualFormData,
	})
	if err != nil {
		respondUserOrderPreviewError(c, err)
		return
	}

	response.Success(c, preview)
}

// GetOrderPaymentChannels 获取当前用户订单可用支付渠道（按金额与商品范围过滤）
func (h *Handler) GetOrderPaymentChannels(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}

	var req OrderPaymentChannelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}

	amount, err := decimal.NewFromString(strings.TrimSpace(req.Amount))
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	if amount.LessThanOrEqual(decimal.Zero) {
		response.Success(c, []map[string]interface{}{})
		return
	}

	user, _ := h.UserRepo.GetByID(uid)
	channels, err := h.PaymentService.GetAvailableChannels(service.AvailablePaymentChannelFilter{
		TargetAmount: &models.Money{Decimal: amount},
		User:         user,
		PaymentType:  constants.PaymentTypeOrder,
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}

	var allowedIDs []uint
	orderNo := strings.TrimSpace(req.OrderNo)
	switch {
	case orderNo != "":
		order, orderErr := h.OrderService.GetOrderByUserOrderNoForTenant(tenantFromRequest(c), orderNo, uid)
		if orderErr != nil {
			if errors.Is(orderErr, service.ErrOrderNotFound) {
				shared.RespondError(c, response.CodeNotFound, "error.order_not_found", nil)
				return
			}
			shared.RespondError(c, response.CodeInternal, "error.order_fetch_failed", orderErr)
			return
		}
		allItems := append([]models.OrderItem{}, order.Items...)
		for _, child := range order.Children {
			allItems = append(allItems, child.Items...)
		}
		allowedIDs = h.PaymentService.GetAllowedChannelIDsForOrder(allItems)
	case len(req.Items) > 0:
		orderItems := make([]models.OrderItem, 0, len(req.Items))
		for _, item := range req.Items {
			if item.ProductID == 0 {
				continue
			}
			orderItems = append(orderItems, models.OrderItem{ProductID: item.ProductID})
		}
		allowedIDs = h.PaymentService.GetAllowedChannelIDsForOrder(orderItems)
	}

	// nil 表示商品未限制渠道；空切片表示限制后无可用渠道。
	if allowedIDs != nil {
		allowedSet := make(map[uint]struct{}, len(allowedIDs))
		for _, id := range allowedIDs {
			allowedSet[id] = struct{}{}
		}
		filtered := make([]map[string]interface{}, 0, len(channels))
		for _, channel := range channels {
			channelID, ok := channel["id"].(uint)
			if !ok {
				continue
			}
			if _, matched := allowedSet[channelID]; matched {
				filtered = append(filtered, channel)
			}
		}
		channels = filtered
	}

	response.Success(c, channels)
}

// CreateOrder 创建订单
func (h *Handler) CreateOrder(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}

	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	tenant := tenantFromRequest(c)

	var items []service.CreateOrderItem
	for _, item := range req.Items {
		items = append(items, service.CreateOrderItem{
			ProductID:       item.ProductID,
			SKUID:           item.SKUID,
			Quantity:        item.Quantity,
			FulfillmentType: item.FulfillmentType,
		})
	}

	order, err := h.OrderService.CreateOrder(service.CreateOrderInput{
		UserID:              uid,
		Tenant:              tenant,
		Items:               items,
		CouponCode:          req.CouponCode,
		AffiliateCode:       req.AffiliateCode,
		AffiliateVisitorKey: req.AffiliateVisitorKey,
		ClientIP:            c.ClientIP(),
		ManualFormData:      req.ManualFormData,
	})
	if err != nil {
		respondUserOrderCreateError(c, err)
		return
	}

	orderDetail := dto.NewOrderDetail(order)
	h.enrichOrderWithAllowedChannels(order, &orderDetail)
	response.Success(c, orderDetail)
}

// CreateOrderAndPayRequest 创建订单并发起支付请求
type CreateOrderAndPayRequest struct {
	Items               []OrderItemRequest     `json:"items" binding:"required"`
	CouponCode          string                 `json:"coupon_code"`
	AffiliateCode       string                 `json:"affiliate_code"`
	AffiliateVisitorKey string                 `json:"affiliate_visitor_key"`
	ManualFormData      map[string]models.JSON `json:"manual_form_data"`
	ChannelID           uint                   `json:"channel_id"`
	UseBalance          bool                   `json:"use_balance"`
}

// CreateOrderAndPay 创建订单并发起支付（合并接口）
func (h *Handler) CreateOrderAndPay(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}

	var req CreateOrderAndPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}
	tenant := tenantFromRequest(c)

	var items []service.CreateOrderItem
	for _, item := range req.Items {
		items = append(items, service.CreateOrderItem{
			ProductID:       item.ProductID,
			SKUID:           item.SKUID,
			Quantity:        item.Quantity,
			FulfillmentType: item.FulfillmentType,
		})
	}

	order, err := h.OrderService.CreateOrder(service.CreateOrderInput{
		UserID:              uid,
		Tenant:              tenant,
		Items:               items,
		CouponCode:          req.CouponCode,
		AffiliateCode:       req.AffiliateCode,
		AffiliateVisitorKey: req.AffiliateVisitorKey,
		ClientIP:            c.ClientIP(),
		ManualFormData:      req.ManualFormData,
	})
	if err != nil {
		respondUserOrderCreateError(c, err)
		return
	}

	orderResp := dto.NewOrderDetail(order)
	h.enrichOrderWithAllowedChannels(order, &orderResp)

	// 如果未指定支付渠道且未使用余额，仅返回订单
	if req.ChannelID == 0 && !req.UseBalance {
		response.Success(c, gin.H{
			"order":    orderResp,
			"order_no": order.OrderNo,
		})
		return
	}

	result, err := h.PaymentService.CreatePayment(service.CreatePaymentInput{
		OrderID:    order.ID,
		ChannelID:  req.ChannelID,
		UseBalance: req.UseBalance,
		ClientIP:   c.ClientIP(),
		Context:    c.Request.Context(),
	})
	if err != nil {
		resp := gin.H{
			"order":         orderResp,
			"order_no":      order.OrderNo,
			"payment_error": err.Error(),
		}
		response.Success(c, resp)
		return
	}

	resp := gin.H{
		"order":              orderResp,
		"order_no":           order.OrderNo,
		"order_paid":         result.OrderPaid,
		"wallet_paid_amount": result.WalletPaidAmount,
		"online_pay_amount":  result.OnlinePayAmount,
	}
	if result.Payment != nil {
		resp["payment_id"] = result.Payment.ID
		resp["provider_type"] = result.Payment.ProviderType
		resp["channel_type"] = result.Payment.ChannelType
		resp["interaction_mode"] = result.Payment.InteractionMode
		resp["pay_url"] = result.Payment.PayURL
		resp["qr_code"] = result.Payment.QRCode
		if info := dto.ExtractCryptoWalletInfo(result.Payment.ProviderType, result.Payment.InteractionMode, result.Payment.ProviderPayload); info.HasAny() {
			if info.Address != "" {
				resp["wallet_address"] = info.Address
			}
			if info.ChainAmount != "" {
				resp["chain_amount"] = info.ChainAmount
			}
			if info.Chain != "" {
				resp["chain"] = info.Chain
			}
			if info.TokenID != "" {
				resp["token_id"] = info.TokenID
			}
		}
		resp["expires_at"] = result.Payment.ExpiredAt
	}
	response.Success(c, resp)
}

// ListOrders 获取订单列表
func (h *Handler) ListOrders(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}

	page, pageSize := shared.ParsePagination(c)

	status := strings.TrimSpace(c.Query("status"))
	orderNo := strings.TrimSpace(c.Query("order_no"))

	orders, total, err := h.OrderService.ListOrdersByUserForTenant(tenantFromRequest(c), repository.OrderListFilter{
		Page:     page,
		PageSize: pageSize,
		UserID:   uid,
		Status:   status,
		OrderNo:  orderNo,
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.order_fetch_failed", err)
		return
	}

	pagination := response.BuildPagination(page, pageSize, total)
	response.SuccessWithPage(c, dto.NewOrderSummaryList(orders), pagination)
}

// OrderStats 按状态聚合当前用户订单数量（基于全量数据，仅复用关键词筛选）
func (h *Handler) OrderStats(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}

	orderNo := strings.TrimSpace(c.Query("order_no"))

	stats, err := h.OrderService.StatsOrdersByUserForTenant(tenantFromRequest(c), repository.OrderListFilter{
		UserID:  uid,
		OrderNo: orderNo,
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.order_fetch_failed", err)
		return
	}

	var total int64
	for _, v := range stats {
		total += v
	}
	response.Success(c, gin.H{
		"total":     total,
		"by_status": stats,
	})
}

// GetOrderByOrderNo 按订单号获取订单详情
func (h *Handler) GetOrderByOrderNo(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}

	orderNo := strings.TrimSpace(c.Param("order_no"))
	if orderNo == "" {
		shared.RespondError(c, response.CodeBadRequest, "error.order_item_invalid", nil)
		return
	}

	order, err := h.OrderService.GetOrderByUserOrderNoForTenant(tenantFromRequest(c), orderNo, uid)
	if err != nil {
		if errors.Is(err, service.ErrOrderNotFound) {
			shared.RespondError(c, response.CodeNotFound, "error.order_not_found", nil)
			return
		}
		shared.RespondError(c, response.CodeInternal, "error.order_fetch_failed", err)
		return
	}

	orderDetail := dto.NewOrderDetailTruncated(order)
	h.enrichOrderWithAllowedChannels(order, &orderDetail)
	h.enrichOrderWithRefundRecords(order, &orderDetail)
	response.Success(c, orderDetail)
}

// CancelOrder 用户取消订单
func (h *Handler) CancelOrder(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}

	orderNo := strings.TrimSpace(c.Param("order_no"))
	if orderNo == "" {
		shared.RespondError(c, response.CodeBadRequest, "error.order_item_invalid", nil)
		return
	}

	tenant := tenantFromRequest(c)
	found, err := h.OrderService.GetOrderByUserOrderNoForTenant(tenant, orderNo, uid)
	if err != nil {
		if errors.Is(err, service.ErrOrderNotFound) {
			shared.RespondError(c, response.CodeNotFound, "error.order_not_found", nil)
			return
		}
		shared.RespondError(c, response.CodeInternal, "error.order_fetch_failed", err)
		return
	}

	order, err := h.OrderService.CancelOrder(found.ID, uid)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrOrderNotFound):
			shared.RespondError(c, response.CodeNotFound, "error.order_not_found", nil)
		case errors.Is(err, service.ErrOrderCancelNotAllowed):
			shared.RespondError(c, response.CodeBadRequest, "error.order_cancel_not_allowed", nil)
		default:
			shared.RespondError(c, response.CodeInternal, "error.order_update_failed", err)
		}
		return
	}

	response.Success(c, dto.NewOrderDetail(order))
}

// DownloadFulfillment 下载订单交付内容（登录用户）
// 支持父订单或子订单的 order_no
func (h *Handler) DownloadFulfillment(c *gin.Context) {
	uid, ok := shared.GetUserID(c)
	if !ok {
		return
	}
	orderNo := strings.TrimSpace(c.Param("order_no"))
	if orderNo == "" {
		shared.RespondError(c, response.CodeBadRequest, "error.order_item_invalid", nil)
		return
	}
	order, err := h.OrderService.GetAnyOrderByUserOrderNoForTenant(tenantFromRequest(c), orderNo, uid)
	if err != nil {
		if errors.Is(err, service.ErrOrderNotFound) {
			shared.RespondError(c, response.CodeNotFound, "error.order_not_found", nil)
			return
		}
		shared.RespondError(c, response.CodeInternal, "error.order_fetch_failed", err)
		return
	}
	if order == nil {
		shared.RespondError(c, response.CodeNotFound, "error.order_not_found", nil)
		return
	}
	respondFulfillmentDownload(c, order)
}

// respondFulfillmentDownload 输出订单交付内容下载响应。
func respondFulfillmentDownload(c *gin.Context, order *models.Order) {
	payload := collectFulfillmentPayload(order)
	if payload == "" {
		shared.RespondError(c, response.CodeNotFound, "error.fulfillment_not_found", nil)
		return
	}
	filename := "fulfillment-" + order.OrderNo + ".txt"
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
	c.Data(200, "text/plain; charset=utf-8", []byte(payload))
}

// collectFulfillmentPayload 汇总父订单与子订单的可下载交付内容。
func collectFulfillmentPayload(order *models.Order) string {
	if order.Fulfillment != nil && order.Fulfillment.Payload != "" {
		return order.Fulfillment.Payload
	}
	var parts []string
	for _, child := range order.Children {
		if child.Fulfillment != nil && child.Fulfillment.Payload != "" {
			parts = append(parts, child.Fulfillment.Payload)
		}
	}
	return strings.Join(parts, "\n")
}
