package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupWalletServiceTest(t *testing.T) (*WalletService, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:wallet_service_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&models.User{},
		&models.Order{},
		&models.OrderItem{},
		&models.Fulfillment{},
		&models.SiteConnection{},
		&models.ProcurementOrder{},
		&models.AffiliateProfile{},
		&models.AffiliateCommission{},
		&models.AffiliateWithdrawRequest{},
		&models.WalletAccount{},
		&models.WalletTransaction{},
		&models.OrderRefundRecord{},
		&models.Setting{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	models.DB = db
	walletRepo := repository.NewWalletRepository(db)
	orderRepo := repository.NewOrderRepository(db)
	refundRecordRepo := repository.NewOrderRefundRecordRepository(db)
	userRepo := repository.NewUserRepository(db)
	affiliateSvc := NewAffiliateService(repository.NewAffiliateRepository(db), nil, nil, nil, nil)
	settingSvc := NewSettingService(repository.NewSettingRepository(db))
	return NewWalletService(walletRepo, orderRepo, refundRecordRepo, userRepo, affiliateSvc, settingSvc), db
}

func createTestUser(t *testing.T, db *gorm.DB, id uint) {
	t.Helper()
	user := models.User{
		ID:           id,
		Email:        fmt.Sprintf("wallet_user_%d@example.com", id),
		PasswordHash: "hash",
		Status:       constants.UserStatusActive,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user failed: %v", err)
	}
}

func createTestOrder(t *testing.T, db *gorm.DB, userID uint, orderNo string, total decimal.Decimal) *models.Order {
	t.Helper()
	now := time.Now()
	order := &models.Order{
		OrderNo:          orderNo,
		UserID:           userID,
		Status:           constants.OrderStatusPendingPayment,
		Currency:         "CNY",
		OriginalAmount:   models.NewMoneyFromDecimal(total),
		DiscountAmount:   models.NewMoneyFromDecimal(decimal.Zero),
		TotalAmount:      models.NewMoneyFromDecimal(total),
		WalletPaidAmount: models.NewMoneyFromDecimal(decimal.Zero),
		OnlinePaidAmount: models.NewMoneyFromDecimal(total),
		RefundedAmount:   models.NewMoneyFromDecimal(decimal.Zero),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	return order
}

func createTestSiteConnection(t *testing.T, db *gorm.DB, id uint) *models.SiteConnection {
	t.Helper()
	conn := &models.SiteConnection{
		ID:        id,
		Name:      fmt.Sprintf("conn-%d", id),
		BaseURL:   "https://upstream.example.com",
		ApiKey:    fmt.Sprintf("key-%d", id),
		ApiSecret: "secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
		Status:    constants.ConnectionStatusActive,
		RetryMax:  3,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := db.Create(conn).Error; err != nil {
		t.Fatalf("create site connection failed: %v", err)
	}
	return conn
}

func createTestChildOrderWithFulfillmentType(
	t *testing.T,
	db *gorm.DB,
	parent *models.Order,
	orderNo string,
	status string,
	total decimal.Decimal,
	fulfillmentType string,
	withFulfillment bool,
) *models.Order {
	t.Helper()
	now := time.Now()
	child := &models.Order{
		OrderNo:          orderNo,
		ParentID:         &parent.ID,
		UserID:           parent.UserID,
		Status:           status,
		Currency:         parent.Currency,
		OriginalAmount:   models.NewMoneyFromDecimal(total),
		DiscountAmount:   models.NewMoneyFromDecimal(decimal.Zero),
		TotalAmount:      models.NewMoneyFromDecimal(total),
		WalletPaidAmount: models.NewMoneyFromDecimal(decimal.Zero),
		OnlinePaidAmount: models.NewMoneyFromDecimal(total),
		RefundedAmount:   models.NewMoneyFromDecimal(decimal.Zero),
		PaidAt:           parent.PaidAt,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(child).Error; err != nil {
		t.Fatalf("create child order failed: %v", err)
	}
	item := &models.OrderItem{
		OrderID:         child.ID,
		ProductID:       child.ID + 1000,
		SKUID:           1,
		TitleJSON:       models.JSON{"zh-CN": orderNo},
		UnitPrice:       models.NewMoneyFromDecimal(total),
		CostPrice:       models.NewMoneyFromDecimal(decimal.Zero),
		Quantity:        1,
		TotalPrice:      models.NewMoneyFromDecimal(total),
		FulfillmentType: fulfillmentType,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(item).Error; err != nil {
		t.Fatalf("create child order item failed: %v", err)
	}
	if withFulfillment {
		fulfillment := &models.Fulfillment{
			OrderID:     child.ID,
			Type:        fulfillmentType,
			Status:      constants.FulfillmentStatusDelivered,
			Payload:     "DELIVERED-CONTENT",
			DeliveredAt: &now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := db.Create(fulfillment).Error; err != nil {
			t.Fatalf("create child fulfillment failed: %v", err)
		}
	}
	return child
}

type walletMixedChildrenRefundFixture struct {
	userID               uint
	parentOrderNo        string
	refundAmount         decimal.Decimal
	remark               string
	expectedParentStatus string
	expectedChildStatus  string
}

func assertWalletMixedChildrenRefundStatus(t *testing.T, fixture walletMixedChildrenRefundFixture) {
	t.Helper()
	svc, db := setupWalletServiceTest(t)
	createTestUser(t, db, fixture.userID)
	parent := createTestOrder(t, db, fixture.userID, fixture.parentOrderNo, decimal.NewFromInt(100))
	paidAt := time.Now()
	if err := db.Model(&models.Order{}).Where("id = ?", parent.ID).Updates(map[string]interface{}{
		"status":  constants.OrderStatusCompleted,
		"paid_at": paidAt,
	}).Error; err != nil {
		t.Fatalf("update parent status failed: %v", err)
	}
	parent.PaidAt = &paidAt

	manualChild := createTestChildOrderWithFulfillmentType(
		t, db, parent, fixture.parentOrderNo+"-1", constants.OrderStatusPaid,
		decimal.NewFromInt(60), constants.FulfillmentTypeManual, false,
	)
	autoChild := createTestChildOrderWithFulfillmentType(
		t, db, parent, fixture.parentOrderNo+"-2", constants.OrderStatusCompleted,
		decimal.NewFromInt(40), constants.FulfillmentTypeAuto, true,
	)

	updatedOrder, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{
		OrderID: parent.ID,
		Amount:  models.NewMoneyFromDecimal(fixture.refundAmount),
		Remark:  fixture.remark,
	})
	if err != nil {
		t.Fatalf("admin refund failed: %v", err)
	}
	if updatedOrder.Status != fixture.expectedParentStatus {
		t.Fatalf("expected parent status %s, got: %s", fixture.expectedParentStatus, updatedOrder.Status)
	}
	assertWalletOrderStatus(t, db, manualChild.ID, "manual child", fixture.expectedChildStatus)
	assertWalletOrderStatus(t, db, autoChild.ID, "auto child", fixture.expectedChildStatus)
}

func assertWalletOrderStatus(t *testing.T, db *gorm.DB, orderID uint, label, expected string) {
	t.Helper()
	var refreshed models.Order
	if err := db.First(&refreshed, orderID).Error; err != nil {
		t.Fatalf("reload %s failed: %v", label, err)
	}
	if refreshed.Status != expected {
		t.Fatalf("expected %s %s, got: %s", label, expected, refreshed.Status)
	}
}

func TestWalletServiceRecharge(t *testing.T) {
	svc, db := setupWalletServiceTest(t)
	createTestUser(t, db, 101)

	account, txn, err := svc.Recharge(WalletRechargeInput{
		UserID: 101,
		Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(120)),
		Remark: "测试充值",
	})
	if err != nil {
		t.Fatalf("recharge failed: %v", err)
	}
	if !account.Balance.Decimal.Equal(decimal.NewFromInt(120)) {
		t.Fatalf("unexpected balance: %s", account.Balance.String())
	}
	if txn == nil || txn.Type != constants.WalletTxnTypeRecharge || txn.Direction != constants.WalletTxnDirectionIn {
		t.Fatalf("unexpected transaction: %+v", txn)
	}
}

func TestWalletServiceAdminAdjustInsufficient(t *testing.T) {
	svc, db := setupWalletServiceTest(t)
	createTestUser(t, db, 102)

	if _, _, err := svc.Recharge(WalletRechargeInput{
		UserID: 102,
		Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
	}); err != nil {
		t.Fatalf("recharge failed: %v", err)
	}

	_, _, err := svc.AdminAdjustBalance(WalletAdjustInput{
		UserID: 102,
		Delta:  models.NewMoneyFromDecimal(decimal.NewFromInt(-20)),
		Remark: "测试扣减",
	})
	if !errors.Is(err, ErrWalletInsufficientBalance) {
		t.Fatalf("expected insufficient balance, got: %v", err)
	}
}

func TestWalletServiceApplyAndReleaseOrderBalance(t *testing.T) {
	svc, db := setupWalletServiceTest(t)
	createTestUser(t, db, 103)
	order := createTestOrder(t, db, 103, "DJTESTAPPLY001", decimal.NewFromInt(30))

	if _, _, err := svc.Recharge(WalletRechargeInput{
		UserID: 103,
		Amount: models.NewMoneyFromDecimal(decimal.NewFromInt(50)),
	}); err != nil {
		t.Fatalf("recharge failed: %v", err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		deducted, err := svc.ApplyOrderBalance(tx, order, true)
		if err != nil {
			return err
		}
		if !deducted.Equal(decimal.NewFromInt(30)) {
			t.Fatalf("expected deducted 30, got %s", deducted.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("apply order balance failed: %v", err)
	}

	account, err := svc.GetAccount(103)
	if err != nil {
		t.Fatalf("get account failed: %v", err)
	}
	if !account.Balance.Decimal.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("unexpected balance after apply: %s", account.Balance.String())
	}

	var refreshed models.Order
	if err := db.First(&refreshed, order.ID).Error; err != nil {
		t.Fatalf("reload order failed: %v", err)
	}
	order.WalletPaidAmount = refreshed.WalletPaidAmount
	order.OnlinePaidAmount = refreshed.OnlinePaidAmount

	if err := db.Transaction(func(tx *gorm.DB) error {
		refunded, err := svc.ReleaseOrderBalance(tx, order, constants.WalletTxnTypeOrderRefund, "测试回退")
		if err != nil {
			return err
		}
		if !refunded.Equal(decimal.NewFromInt(30)) {
			t.Fatalf("expected refunded 30, got %s", refunded.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("release order balance failed: %v", err)
	}

	account, err = svc.GetAccount(103)
	if err != nil {
		t.Fatalf("get account failed: %v", err)
	}
	if !account.Balance.Decimal.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("unexpected balance after release: %s", account.Balance.String())
	}
}

func TestWalletServiceAdminRefundToWallet(t *testing.T) {
	svc, db := setupWalletServiceTest(t)
	createTestUser(t, db, 104)
	createTestUser(t, db, 204)
	order := createTestOrder(t, db, 104, "DJTESTREFUND001", decimal.NewFromInt(40))
	paidAt := time.Now()
	if err := db.Model(&models.Order{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
		"status":  constants.OrderStatusPaid,
		"paid_at": paidAt,
	}).Error; err != nil {
		t.Fatalf("update order status failed: %v", err)
	}
	profile := models.AffiliateProfile{
		UserID:        204,
		AffiliateCode: "AFFT104A",
		Status:        constants.AffiliateProfileStatusActive,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create affiliate profile failed: %v", err)
	}
	commission := models.AffiliateCommission{
		AffiliateProfileID: profile.ID,
		OrderID:            order.ID,
		CommissionType:     constants.AffiliateCommissionTypeOrder,
		BaseAmount:         models.NewMoneyFromDecimal(decimal.NewFromInt(40)),
		RatePercent:        models.NewMoneyFromDecimal(decimal.NewFromInt(50)),
		CommissionAmount:   models.NewMoneyFromDecimal(decimal.NewFromInt(20)),
		Status:             constants.AffiliateCommissionStatusAvailable,
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}
	if err := db.Create(&commission).Error; err != nil {
		t.Fatalf("create affiliate commission failed: %v", err)
	}

	updatedOrder, txn, createdRecord, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{
		OrderID: order.ID,
		Amount:  models.NewMoneyFromDecimal(decimal.NewFromInt(15)),
		Remark:  "测试退款",
	})
	if err != nil {
		t.Fatalf("admin refund failed: %v", err)
	}
	if txn == nil || txn.Type != constants.WalletTxnTypeAdminRefund {
		t.Fatalf("unexpected refund transaction: %+v", txn)
	}
	if createdRecord == nil || createdRecord.ID == 0 {
		t.Fatalf("expected created refund record, got %+v", createdRecord)
	}
	if !updatedOrder.RefundedAmount.Decimal.Equal(decimal.NewFromInt(15)) {
		t.Fatalf("unexpected refunded amount: %s", updatedOrder.RefundedAmount.String())
	}
	if updatedOrder.Status != constants.OrderStatusPartiallyRefunded {
		t.Fatalf("expected status partially_refunded, got: %s", updatedOrder.Status)
	}
	var refundRecord models.OrderRefundRecord
	if err := db.Where("order_id = ? AND type = ?", order.ID, constants.OrderRefundTypeWallet).
		Order("id desc").
		First(&refundRecord).Error; err != nil {
		t.Fatalf("query order refund record failed: %v", err)
	}
	if !refundRecord.Amount.Decimal.Equal(decimal.NewFromInt(15)) {
		t.Fatalf("unexpected refund record amount: %s", refundRecord.Amount.String())
	}
	var refreshedCommission models.AffiliateCommission
	if err := db.First(&refreshedCommission, commission.ID).Error; err != nil {
		t.Fatalf("reload affiliate commission failed: %v", err)
	}
	if !refreshedCommission.CommissionAmount.Decimal.Equal(decimal.RequireFromString("12.50")) {
		t.Fatalf("unexpected commission amount after refund: %s", refreshedCommission.CommissionAmount.String())
	}
	if refreshedCommission.Status != constants.AffiliateCommissionStatusAvailable {
		t.Fatalf("unexpected commission status after partial refund: %s", refreshedCommission.Status)
	}

	_, _, _, err = svc.AdminRefundToWallet(AdminRefundToWalletInput{
		OrderID: order.ID,
		Amount:  models.NewMoneyFromDecimal(decimal.NewFromInt(30)),
		Remark:  "超额退款",
	})
	if !errors.Is(err, ErrWalletRefundExceeded) {
		t.Fatalf("expected refund exceeded, got: %v", err)
	}
}

func TestWalletServiceAdminRefundToWalletRejectUnpaidOrder(t *testing.T) {
	svc, db := setupWalletServiceTest(t)
	createTestUser(t, db, 105)
	order := createTestOrder(t, db, 105, "DJTESTREFUND002", decimal.NewFromInt(40))
	if err := db.Model(&models.Order{}).Where("id = ?", order.ID).Update("status", constants.OrderStatusCanceled).Error; err != nil {
		t.Fatalf("update order status failed: %v", err)
	}

	_, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{
		OrderID: order.ID,
		Amount:  models.NewMoneyFromDecimal(decimal.NewFromInt(15)),
		Remark:  "未支付退款",
	})
	if !errors.Is(err, ErrOrderStatusInvalid) {
		t.Fatalf("expected order status invalid, got: %v", err)
	}
}

func TestWalletServiceAdminRefundToWalletExpiredWindow(t *testing.T) {
	svc, db := setupWalletServiceTest(t)
	createTestUser(t, db, 111)
	order := createTestOrder(t, db, 111, "DJTESTREFUND-EXPIRED", decimal.NewFromInt(40))
	paidAt := time.Now().AddDate(0, 0, -31)
	if err := db.Model(&models.Order{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
		"status":  constants.OrderStatusCompleted,
		"paid_at": paidAt,
	}).Error; err != nil {
		t.Fatalf("update order status failed: %v", err)
	}

	_, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{
		OrderID: order.ID,
		Amount:  models.NewMoneyFromDecimal(decimal.NewFromInt(15)),
		Remark:  "超时退款",
	})
	if !errors.Is(err, ErrOrderRefundExpired) {
		t.Fatalf("expected order refund expired, got: %v", err)
	}
}

func TestWalletServiceAdminRefundToWalletNoLimitWhenZero(t *testing.T) {
	svc, db := setupWalletServiceTest(t)
	createTestUser(t, db, 116)
	order := createTestOrder(t, db, 116, "DJTESTREFUND-NOLIMIT", decimal.NewFromInt(40))
	paidAt := time.Now().AddDate(0, 0, -90)
	if err := db.Model(&models.Order{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
		"status":  constants.OrderStatusCompleted,
		"paid_at": paidAt,
	}).Error; err != nil {
		t.Fatalf("update order status failed: %v", err)
	}

	if _, err := svc.settingService.Update(constants.SettingKeyOrderConfig, map[string]interface{}{
		orderConfigFieldMaxRefundDays: 0,
	}); err != nil {
		t.Fatalf("update order refund config failed: %v", err)
	}

	updatedOrder, txn, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{
		OrderID: order.ID,
		Amount:  models.NewMoneyFromDecimal(decimal.NewFromInt(15)),
		Remark:  "0天不限制",
	})
	if err != nil {
		t.Fatalf("expected no-limit refund success, got: %v", err)
	}
	if txn == nil {
		t.Fatalf("expected transaction, got nil")
	}
	if updatedOrder == nil || updatedOrder.Status != constants.OrderStatusPartiallyRefunded {
		t.Fatalf("expected partially_refunded order, got %+v", updatedOrder)
	}
}

func TestWalletServiceAdminRefundToWalletCompletedOrderPartialSetsPartiallyRefunded(t *testing.T) {
	svc, db := setupWalletServiceTest(t)
	createTestUser(t, db, 112)
	order := createTestOrder(t, db, 112, "DJTESTREFUND003", decimal.NewFromInt(40))
	conn := createTestSiteConnection(t, db, 1)
	paidAt := time.Now()
	if err := db.Model(&models.Order{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
		"status":  constants.OrderStatusCompleted,
		"paid_at": paidAt,
	}).Error; err != nil {
		t.Fatalf("update order status failed: %v", err)
	}
	proc := &models.ProcurementOrder{
		ConnectionID:    conn.ID,
		LocalOrderID:    order.ID,
		LocalOrderNo:    order.OrderNo,
		Status:          constants.ProcurementStatusFulfilled,
		LocalSellAmount: models.NewMoneyFromDecimal(order.TotalAmount.Decimal),
		Currency:        order.Currency,
		TraceID:         "wallet-refund-proc-sync",
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := db.Create(proc).Error; err != nil {
		t.Fatalf("create procurement order failed: %v", err)
	}

	updatedOrder, txn, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{
		OrderID: order.ID,
		Amount:  models.NewMoneyFromDecimal(decimal.NewFromInt(10)),
		Remark:  "已完成订单部分退款",
	})
	if err != nil {
		t.Fatalf("admin refund failed: %v", err)
	}
	if txn == nil {
		t.Fatalf("expected transaction, got nil")
	}
	if !updatedOrder.RefundedAmount.Decimal.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("unexpected refunded amount: %s", updatedOrder.RefundedAmount.String())
	}
	if updatedOrder.Status != constants.OrderStatusPartiallyRefunded {
		t.Fatalf("expected status partially_refunded, got: %s", updatedOrder.Status)
	}
	var refreshedProc models.ProcurementOrder
	if err := db.First(&refreshedProc, proc.ID).Error; err != nil {
		t.Fatalf("reload procurement order failed: %v", err)
	}
	if refreshedProc.Status != constants.ProcurementStatusFulfilled {
		t.Fatalf("expected procurement status fulfilled, got: %s", refreshedProc.Status)
	}
}

func TestWalletServiceAdminRefundToWalletFullRefundUpdatesChildrenStatus(t *testing.T) {
	svc, db := setupWalletServiceTest(t)
	createTestUser(t, db, 107)
	parent := createTestOrder(t, db, 107, "DJTESTREFUNDCHILD001", decimal.NewFromInt(30))
	paidAt := time.Now()
	if err := db.Model(&models.Order{}).Where("id = ?", parent.ID).Updates(map[string]interface{}{
		"status":  constants.OrderStatusDelivered,
		"paid_at": paidAt,
	}).Error; err != nil {
		t.Fatalf("update parent status failed: %v", err)
	}
	child := &models.Order{
		OrderNo:          "DJTESTREFUNDCHILD001-1",
		ParentID:         &parent.ID,
		UserID:           parent.UserID,
		Status:           constants.OrderStatusFulfilling,
		Currency:         parent.Currency,
		OriginalAmount:   models.NewMoneyFromDecimal(decimal.NewFromInt(30)),
		DiscountAmount:   models.NewMoneyFromDecimal(decimal.Zero),
		TotalAmount:      models.NewMoneyFromDecimal(decimal.NewFromInt(30)),
		WalletPaidAmount: models.NewMoneyFromDecimal(decimal.Zero),
		OnlinePaidAmount: models.NewMoneyFromDecimal(decimal.NewFromInt(30)),
		RefundedAmount:   models.NewMoneyFromDecimal(decimal.Zero),
		PaidAt:           &paidAt,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := db.Create(child).Error; err != nil {
		t.Fatalf("create child order failed: %v", err)
	}

	_, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{
		OrderID: parent.ID,
		Amount:  models.NewMoneyFromDecimal(decimal.NewFromInt(30)),
		Remark:  "全额退款",
	})
	if err != nil {
		t.Fatalf("admin refund failed: %v", err)
	}

	var refreshedChild models.Order
	if err := db.First(&refreshedChild, child.ID).Error; err != nil {
		t.Fatalf("reload child order failed: %v", err)
	}
	if refreshedChild.Status != constants.OrderStatusRefunded {
		t.Fatalf("expected child status refunded, got: %s", refreshedChild.Status)
	}
}

func TestWalletServiceAdminRefundToWalletParentPartialMixedChildrenStatus(t *testing.T) {
	assertWalletMixedChildrenRefundStatus(t, walletMixedChildrenRefundFixture{
		userID:               207,
		parentOrderNo:        "DJTESTREFUNDMIXED-PARTIAL",
		refundAmount:         decimal.NewFromInt(20),
		remark:               "混合子订单部分退款",
		expectedParentStatus: constants.OrderStatusPartiallyRefunded,
		expectedChildStatus:  constants.OrderStatusPartiallyRefunded,
	})
}

func TestWalletServiceAdminRefundToWalletParentFullMixedChildrenStatus(t *testing.T) {
	assertWalletMixedChildrenRefundStatus(t, walletMixedChildrenRefundFixture{
		userID:               208,
		parentOrderNo:        "DJTESTREFUNDMIXED-FULL",
		refundAmount:         decimal.NewFromInt(100),
		remark:               "混合子订单全额退款",
		expectedParentStatus: constants.OrderStatusRefunded,
		expectedChildStatus:  constants.OrderStatusRefunded,
	})
}
