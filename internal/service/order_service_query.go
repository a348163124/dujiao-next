package service

import (
	"sort"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"

	"github.com/shopspring/decimal"
)

// BuildLocalRefundRecordsForOrder 构建订单关联的本地退款记录列表。
// 仅返回本地 order_refund_records 数据，不透传更上游退款记录。
func (s *OrderService) BuildLocalRefundRecordsForOrder(order *models.Order) ([]models.JSON, error) {
	recordsJSON := make([]models.JSON, 0)
	if order == nil || s.orderRefundRecordRepo == nil {
		return recordsJSON, nil
	}

	idSet := make(map[uint]struct{}, 4)
	idSet[order.ID] = struct{}{}
	if order.ParentID != nil && *order.ParentID > 0 {
		idSet[*order.ParentID] = struct{}{}
	}
	for i := range order.Children {
		if order.Children[i].ID > 0 {
			idSet[order.Children[i].ID] = struct{}{}
		}
	}

	orderIDs := make([]uint, 0, len(idSet))
	for id := range idSet {
		orderIDs = append(orderIDs, id)
	}

	records, err := s.orderRefundRecordRepo.ListByOrderIDs(orderIDs)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return recordsJSON, nil
	}

	sort.SliceStable(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})

	recordsJSON = make([]models.JSON, 0, len(records))
	for idx, record := range records {
		recordsJSON = append(recordsJSON, models.JSON{
			// 不暴露内部退款主键，统一返回列表序号。
			"id":          idx + 1,
			"user_id":     record.UserID,
			"guest_email": record.GuestEmail,
			"order_id":    record.OrderID,
			"type":        record.Type,
			"amount":      record.Amount,
			"currency":    record.Currency,
			"remark":      record.Remark,
			"created_at":  record.CreatedAt,
			"updated_at":  record.UpdatedAt,
		})
	}
	return recordsJSON, nil
}

// ensureOrderCanceledIfExpired 读取时懒同步过期订单状态
func (s *OrderService) ensureOrderCanceledIfExpired(order *models.Order) error {
	if order == nil {
		return nil
	}
	if order.Status != constants.OrderStatusPendingPayment {
		return nil
	}
	if order.ExpiresAt == nil {
		return nil
	}
	if order.ExpiresAt.After(time.Now()) {
		return nil
	}
	if err := s.cancelOrderWithChildren(order, true); err != nil {
		return err
	}
	if s.queueClient != nil {
		if _, err := enqueueOrderStatusEmailTaskIfEligible(s.orderRepo, s.queueClient, s.settingService, s.defaultEmailConfig, order.ID, constants.OrderStatusCanceled); err != nil {
			logger.Warnw("order_enqueue_status_email_failed",
				"order_id", order.ID,
				"target_order_id", order.ID,
				"status", constants.OrderStatusCanceled,
				"error", err,
			)
		}
	}
	return nil
}

// ensureOrdersCanceledIfExpired 批量懒同步过期订单状态
func (s *OrderService) ensureOrdersCanceledIfExpired(orders []models.Order) error {
	if len(orders) == 0 {
		return nil
	}
	for i := range orders {
		if err := s.ensureOrderCanceledIfExpired(&orders[i]); err != nil {
			return err
		}
	}
	return nil
}

// expectedRefundStatus 根据订单总额与已退款金额计算应处于的退款状态。
func expectedRefundStatus(order *models.Order) string {
	if order == nil {
		return ""
	}
	if strings.ToLower(strings.TrimSpace(order.Status)) == constants.OrderStatusCanceled {
		return ""
	}
	if order.PaidAt == nil {
		return ""
	}
	total := order.TotalAmount.Decimal.Round(2)
	if total.LessThanOrEqual(decimal.Zero) {
		return ""
	}
	refunded := order.RefundedAmount.Decimal.Round(2)
	if refunded.LessThanOrEqual(decimal.Zero) {
		return ""
	}
	if refunded.GreaterThanOrEqual(total) {
		return constants.OrderStatusRefunded
	}
	return constants.OrderStatusPartiallyRefunded
}

// resolvedParentStatus 计算父订单当前应同步的状态（优先退款状态）。
func resolvedParentStatus(order *models.Order) string {
	if order == nil {
		return ""
	}
	if refundStatus := expectedRefundStatus(order); refundStatus != "" {
		return refundStatus
	}
	return calcParentStatus(order.Children, order.Status)
}

// ensureSingleOrderRefundStatusSynced 懒同步单条订单退款状态到最新值。
func (s *OrderService) ensureSingleOrderRefundStatusSynced(order *models.Order, now time.Time) (bool, error) {
	target := expectedRefundStatus(order)
	if target == "" || strings.EqualFold(strings.TrimSpace(order.Status), target) {
		return false, nil
	}
	if err := s.orderRepo.UpdateStatus(order.ID, target, map[string]interface{}{
		"updated_at": now,
	}); err != nil {
		return false, err
	}
	order.Status = target
	order.UpdatedAt = now
	return true, nil
}

// ensureOrderRefundStatusSynced 读取时懒同步退款相关状态
func (s *OrderService) ensureOrderRefundStatusSynced(order *models.Order) error {
	if order == nil {
		return nil
	}

	now := time.Now()
	orderChanged, err := s.ensureSingleOrderRefundStatusSynced(order, now)
	if err != nil {
		return err
	}

	for i := range order.Children {
		changed, childErr := s.ensureSingleOrderRefundStatusSynced(&order.Children[i], now)
		if childErr != nil {
			return childErr
		}
		if changed {
			orderChanged = true
		}
	}

	if len(order.Children) > 0 {
		parentStatus := resolvedParentStatus(order)
		if !strings.EqualFold(strings.TrimSpace(order.Status), parentStatus) {
			if err := s.orderRepo.UpdateStatus(order.ID, parentStatus, map[string]interface{}{
				"updated_at": now,
			}); err != nil {
				return err
			}
			order.Status = parentStatus
			order.UpdatedAt = now
		}
		return nil
	}

	if order.ParentID != nil && orderChanged {
		if _, err := syncParentStatus(s.orderRepo, *order.ParentID, now); err != nil {
			return err
		}
	}
	return nil
}

// ensureOrdersRefundStatusSynced 批量懒同步退款相关状态
func (s *OrderService) ensureOrdersRefundStatusSynced(orders []models.Order) error {
	if len(orders) == 0 {
		return nil
	}
	for i := range orders {
		if err := s.ensureOrderRefundStatusSynced(&orders[i]); err != nil {
			return err
		}
	}
	return nil
}

// GetOrderByUser 获取订单详情
func (s *OrderService) GetOrderByUser(orderID uint, userID uint) (*models.Order, error) {
	order, err := s.orderRepo.GetByIDAndUser(orderID, userID)
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrOrderNotFound
	}
	if err := s.ensureOrderCanceledIfExpired(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	if err := s.ensureOrderRefundStatusSynced(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	fillOrderItemsFromChildren(order)
	return order, nil
}

// GetOrderByUserForTenant 获取当前租户上下文中的用户订单详情。
func (s *OrderService) GetOrderByUserForTenant(tenant TenantContext, orderID uint, userID uint) (*models.Order, error) {
	order, err := s.orderRepo.GetByIDAndUserScoped(orderID, userID, orderScopeFromTenant(tenant))
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrOrderNotFound
	}
	if err := s.ensureOrderCanceledIfExpired(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	if err := s.ensureOrderRefundStatusSynced(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	fillOrderItemsFromChildren(order)
	return order, nil
}

// GetOrderByUserOrderNo 按订单号获取用户订单详情
func (s *OrderService) GetOrderByUserOrderNo(orderNo string, userID uint) (*models.Order, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, ErrOrderNotFound
	}
	order, err := s.orderRepo.GetByOrderNoAndUser(orderNo, userID)
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrOrderNotFound
	}
	if err := s.ensureOrderCanceledIfExpired(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	if err := s.ensureOrderRefundStatusSynced(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	fillOrderItemsFromChildren(order)
	return order, nil
}

// GetOrderByUserOrderNoForTenant 按订单号获取当前租户上下文中的用户订单详情。
func (s *OrderService) GetOrderByUserOrderNoForTenant(tenant TenantContext, orderNo string, userID uint) (*models.Order, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, ErrOrderNotFound
	}
	order, err := s.orderRepo.GetByOrderNoAndUserScoped(orderNo, userID, orderScopeFromTenant(tenant))
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrOrderNotFound
	}
	if err := s.ensureOrderCanceledIfExpired(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	if err := s.ensureOrderRefundStatusSynced(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	fillOrderItemsFromChildren(order)
	return order, nil
}

// GetAnyOrderByUserOrderNoForTenant 支持父订单或子订单号获取当前租户上下文中的用户订单。
func (s *OrderService) GetAnyOrderByUserOrderNoForTenant(tenant TenantContext, orderNo string, userID uint) (*models.Order, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, ErrOrderNotFound
	}
	order, err := s.orderRepo.GetAnyByOrderNoAndUserScoped(orderNo, userID, orderScopeFromTenant(tenant))
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrOrderNotFound
	}
	if err := s.ensureOrderCanceledIfExpired(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	if err := s.ensureOrderRefundStatusSynced(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	fillOrderItemsFromChildren(order)
	return order, nil
}

// GetOrderByGuest 获取游客订单详情
func (s *OrderService) GetOrderByGuest(orderID uint, email, password string) (*models.Order, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	order, err := s.orderRepo.GetByIDAndGuest(orderID, email, password)
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrGuestOrderNotFound
	}
	if err := s.ensureOrderCanceledIfExpired(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	if err := s.ensureOrderRefundStatusSynced(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	fillOrderItemsFromChildren(order)
	return order, nil
}

// GetOrderByGuestForTenant 获取当前租户上下文中的游客订单详情。
func (s *OrderService) GetOrderByGuestForTenant(tenant TenantContext, orderID uint, email, password string) (*models.Order, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	order, err := s.orderRepo.GetByIDAndGuestScoped(orderID, email, password, orderScopeFromTenant(tenant))
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrGuestOrderNotFound
	}
	if err := s.ensureOrderCanceledIfExpired(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	if err := s.ensureOrderRefundStatusSynced(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	fillOrderItemsFromChildren(order)
	return order, nil
}

// GetOrderByGuestOrderNo 获取游客订单详情（按订单号）
func (s *OrderService) GetOrderByGuestOrderNo(orderNo, email, password string) (*models.Order, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	order, err := s.orderRepo.GetByOrderNoAndGuest(orderNo, email, password)
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrGuestOrderNotFound
	}
	if err := s.ensureOrderCanceledIfExpired(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	if err := s.ensureOrderRefundStatusSynced(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	fillOrderItemsFromChildren(order)
	return order, nil
}

// GetOrderByGuestOrderNoForTenant 获取当前租户上下文中的游客订单详情（按订单号）。
func (s *OrderService) GetOrderByGuestOrderNoForTenant(tenant TenantContext, orderNo, email, password string) (*models.Order, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	order, err := s.orderRepo.GetByOrderNoAndGuestScoped(orderNo, email, password, orderScopeFromTenant(tenant))
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrGuestOrderNotFound
	}
	if err := s.ensureOrderCanceledIfExpired(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	if err := s.ensureOrderRefundStatusSynced(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	fillOrderItemsFromChildren(order)
	return order, nil
}

// GetAnyOrderByGuestOrderNoForTenant 支持父订单或子订单号获取当前租户上下文中的游客订单。
func (s *OrderService) GetAnyOrderByGuestOrderNoForTenant(tenant TenantContext, orderNo, email, password string) (*models.Order, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	order, err := s.orderRepo.GetAnyByOrderNoAndGuestScoped(orderNo, email, password, orderScopeFromTenant(tenant))
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrGuestOrderNotFound
	}
	if err := s.ensureOrderCanceledIfExpired(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	if err := s.ensureOrderRefundStatusSynced(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	fillOrderItemsFromChildren(order)
	return order, nil
}

// ListOrdersByUser 获取订单列表
func (s *OrderService) ListOrdersByUser(filter repository.OrderListFilter) ([]models.Order, int64, error) {
	if filter.UserID == 0 {
		return nil, 0, ErrOrderFetchFailed
	}
	orders, total, err := s.orderRepo.ListByUser(filter)
	if err != nil {
		return nil, 0, ErrOrderFetchFailed
	}
	if err := s.ensureOrdersCanceledIfExpired(orders); err != nil {
		return nil, 0, ErrOrderUpdateFailed
	}
	if err := s.ensureOrdersRefundStatusSynced(orders); err != nil {
		return nil, 0, ErrOrderUpdateFailed
	}
	fillOrdersItemsFromChildren(orders)
	return orders, total, nil
}

// ListOrdersByUserForTenant 获取当前租户上下文中的用户订单列表。
func (s *OrderService) ListOrdersByUserForTenant(tenant TenantContext, filter repository.OrderListFilter) ([]models.Order, int64, error) {
	if filter.UserID == 0 {
		return nil, 0, ErrOrderFetchFailed
	}
	orders, total, err := s.orderRepo.ListByUserScoped(filter, orderScopeFromTenant(tenant))
	if err != nil {
		return nil, 0, ErrOrderFetchFailed
	}
	if err := s.ensureOrdersCanceledIfExpired(orders); err != nil {
		return nil, 0, ErrOrderUpdateFailed
	}
	if err := s.ensureOrdersRefundStatusSynced(orders); err != nil {
		return nil, 0, ErrOrderUpdateFailed
	}
	fillOrdersItemsFromChildren(orders)
	return orders, total, nil
}

// StatsOrdersByUser 按状态聚合用户订单数量（基于全量数据，仅复用关键词筛选）
func (s *OrderService) StatsOrdersByUser(filter repository.OrderListFilter) (map[string]int64, error) {
	if filter.UserID == 0 {
		return nil, ErrOrderFetchFailed
	}
	stats, err := s.orderRepo.StatsByUser(filter)
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	return stats, nil
}

// StatsOrdersByUserForTenant 按状态聚合当前租户上下文中的用户订单数量。
func (s *OrderService) StatsOrdersByUserForTenant(tenant TenantContext, filter repository.OrderListFilter) (map[string]int64, error) {
	if filter.UserID == 0 {
		return nil, ErrOrderFetchFailed
	}
	stats, err := s.orderRepo.StatsByUserScoped(filter, orderScopeFromTenant(tenant))
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	return stats, nil
}

// ListOrdersByGuest 获取游客订单列表
func (s *OrderService) ListOrdersByGuest(email, password string, page, pageSize int) ([]models.Order, int64, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	orders, total, err := s.orderRepo.ListByGuest(email, password, page, pageSize)
	if err != nil {
		return nil, 0, ErrOrderFetchFailed
	}
	if err := s.ensureOrdersCanceledIfExpired(orders); err != nil {
		return nil, 0, ErrOrderUpdateFailed
	}
	if err := s.ensureOrdersRefundStatusSynced(orders); err != nil {
		return nil, 0, ErrOrderUpdateFailed
	}
	fillOrdersItemsFromChildren(orders)
	return orders, total, nil
}

// ListOrdersByGuestForTenant 获取当前租户上下文中的游客订单列表。
func (s *OrderService) ListOrdersByGuestForTenant(tenant TenantContext, email, password string, page, pageSize int) ([]models.Order, int64, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	orders, total, err := s.orderRepo.ListByGuestScoped(email, password, page, pageSize, orderScopeFromTenant(tenant))
	if err != nil {
		return nil, 0, ErrOrderFetchFailed
	}
	if err := s.ensureOrdersCanceledIfExpired(orders); err != nil {
		return nil, 0, ErrOrderUpdateFailed
	}
	if err := s.ensureOrdersRefundStatusSynced(orders); err != nil {
		return nil, 0, ErrOrderUpdateFailed
	}
	fillOrdersItemsFromChildren(orders)
	return orders, total, nil
}

func orderScopeFromTenant(tenant TenantContext) repository.ResellerOrderScope {
	if isResellerOrderContext(tenant) && tenant.ResellerID != nil {
		return repository.ResellerOrderScope{ResellerID: tenant.ResellerID}
	}
	return repository.ResellerOrderScope{}
}

// ListOrdersForAdmin 管理端订单列表
func (s *OrderService) ListOrdersForAdmin(filter repository.OrderListFilter) ([]models.Order, int64, error) {
	orders, total, err := s.orderRepo.ListAdmin(filter)
	if err != nil {
		return nil, 0, ErrOrderFetchFailed
	}
	if err := s.ensureOrdersCanceledIfExpired(orders); err != nil {
		return nil, 0, ErrOrderUpdateFailed
	}
	if err := s.ensureOrdersRefundStatusSynced(orders); err != nil {
		return nil, 0, ErrOrderUpdateFailed
	}
	fillOrdersItemsFromChildren(orders)
	return orders, total, nil
}

// GetOrderForAdmin 管理端订单详情
func (s *OrderService) GetOrderForAdmin(orderID uint) (*models.Order, error) {
	if orderID == 0 {
		return nil, ErrOrderNotFound
	}
	order, err := s.orderRepo.GetByID(orderID)
	if err != nil {
		return nil, ErrOrderFetchFailed
	}
	if order == nil {
		return nil, ErrOrderNotFound
	}
	if err := s.ensureOrderCanceledIfExpired(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	if err := s.ensureOrderRefundStatusSynced(order); err != nil {
		return nil, ErrOrderUpdateFailed
	}
	fillOrderItemsFromChildren(order)
	return order, nil
}
