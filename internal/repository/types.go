package repository

import (
	"time"

	"github.com/shopspring/decimal"
)

// Pagination 通用分页参数
type Pagination struct {
	Page     int
	PageSize int
}

// ProductListFilter 查询商品列表的过滤条件
type ProductListFilter struct {
	Page               int
	PageSize           int
	CategoryID         string
	CategoryIDs        []uint
	ExcludeProductIDs  []uint
	Search             string
	FulfillmentType    string
	StockStatus        string
	HasWholesalePrices *bool
	LowStockThreshold  int // 低库存阈值
	OnlyActive         bool
	WithCategory       bool
	UpdatedAfter       *time.Time // 仅返回此时间之后更新的商品
}

// PostListFilter 查询文章列表的过滤条件
type PostListFilter struct {
	Page          int
	PageSize      int
	Type          string
	Search        string
	OnlyPublished bool
	OrderBy       string
}

// BannerListFilter 查询 Banner 列表的过滤条件
type BannerListFilter struct {
	Page      int
	PageSize  int
	Position  string
	Search    string
	IsActive  *bool
	OrderBy   string
	OnlyValid bool
}

// OrderListFilter 查询订单列表的过滤条件
type OrderListFilter struct {
	Page           int
	PageSize       int
	UserID         uint
	UserKeyword    string
	Status         string
	OrderNo        string
	GuestEmail     string
	ProductKeyword string
	CreatedFrom    *time.Time
	CreatedTo      *time.Time
	SortBy         string
	SortOrder      string
}

// ResellerOrderScope 表示前台订单查询的分销租户范围。
//
// ResellerID == nil 明确表示主站范围: orders.reseller_id IS NULL。
// 后台列表不要使用该结构，后台 nil 语义是“不按分销商过滤”。
type ResellerOrderScope struct {
	ResellerID *uint
}

// PaymentListFilter 查询支付列表的过滤条件
type PaymentListFilter struct {
	Page         int
	PageSize     int
	UserID       uint
	OrderID      uint
	ChannelID    uint
	ProviderType string
	ChannelType  string
	Status       string
	CreatedFrom  *time.Time
	CreatedTo    *time.Time
	SkipCount    bool
	Lightweight  bool
}

// OrderRefundRecordListFilter 查询订单退款记录列表的过滤条件
type OrderRefundRecordListFilter struct {
	Page           int
	PageSize       int
	UserID         uint
	UserKeyword    string
	OrderNo        string
	GuestEmail     string
	ProductKeyword string
	CreatedFrom    *time.Time
	CreatedTo      *time.Time
}

// PaymentChannelListFilter 查询支付渠道列表的过滤条件
type PaymentChannelListFilter struct {
	Page         int
	PageSize     int
	ProviderType string
	ChannelType  string
	ActiveOnly   bool
}

// CouponUsageListFilter 查询优惠券使用记录列表的过滤条件
type CouponUsageListFilter struct {
	Page     int
	PageSize int
	UserID   uint
}

// UserListFilter 查询用户列表的过滤条件
type UserListFilter struct {
	Page          int
	PageSize      int
	UserID        uint
	Keyword       string
	Status        string
	CreatedFrom   *time.Time
	CreatedTo     *time.Time
	LastLoginFrom *time.Time
	LastLoginTo   *time.Time
	SortBy        string // 排序字段：created_at / last_login_at / wallet_balance，其它值回退默认
	SortOrder     string // 排序方向：asc / desc（默认 desc）
}

// WalletAccountListFilter 查询钱包账户列表的过滤条件
type WalletAccountListFilter struct {
	Page     int
	PageSize int
	UserID   uint
}

// WalletTransactionListFilter 查询钱包流水列表的过滤条件
type WalletTransactionListFilter struct {
	Page        int
	PageSize    int
	UserID      uint
	OrderID     uint
	Type        string
	Direction   string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

// WalletRechargeListFilter 查询钱包充值单列表的过滤条件
type WalletRechargeListFilter struct {
	Page         int
	PageSize     int
	RechargeNo   string
	UserID       uint
	UserKeyword  string
	PaymentID    uint
	ChannelID    uint
	ProviderType string
	ChannelType  string
	Status       string
	CreatedFrom  *time.Time
	CreatedTo    *time.Time
	PaidFrom     *time.Time
	PaidTo       *time.Time
}

// UserLoginLogListFilter 查询用户登录日志列表的过滤条件
type UserLoginLogListFilter struct {
	Page        int
	PageSize    int
	UserID      uint
	Email       string
	Status      string
	FailReason  string
	ClientIP    string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

// AuthzAuditLogListFilter 查询权限审计日志列表的过滤条件
type AuthzAuditLogListFilter struct {
	Page            int
	PageSize        int
	OperatorAdminID uint
	TargetAdminID   uint
	Action          string
	Role            string
	Object          string
	Method          string
	CreatedFrom     *time.Time
	CreatedTo       *time.Time
}

// AdminLoginLogListFilter 查询后台管理员登录日志列表的过滤条件
type AdminLoginLogListFilter struct {
	Page      int
	PageSize  int
	AdminID   *uint
	Username  string
	EventType string
	Status    string
}

// NotificationLogListFilter 查询通知发送日志列表的过滤条件
type NotificationLogListFilter struct {
	Page        int
	PageSize    int
	Channel     string
	Status      string
	EventType   string
	IsTest      *bool
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

// AffiliateProfileListFilter 推广用户列表过滤条件
type AffiliateProfileListFilter struct {
	Page     int
	PageSize int
	UserID   uint
	Status   string
	Code     string
	Keyword  string
}

// AffiliateCommissionListFilter 推广佣金列表过滤条件
type AffiliateCommissionListFilter struct {
	Page               int
	PageSize           int
	AffiliateProfileID uint
	OrderID            uint
	OrderNo            string
	Status             string
	Keyword            string
	CreatedFrom        *time.Time
	CreatedTo          *time.Time
}

// AffiliateWithdrawListFilter 推广提现列表过滤条件
type AffiliateWithdrawListFilter struct {
	Page               int
	PageSize           int
	AffiliateProfileID uint
	Status             string
	Keyword            string
	CreatedFrom        *time.Time
	CreatedTo          *time.Time
}

// MediaListFilter 查询素材列表的过滤条件
type MediaListFilter struct {
	Page     int
	PageSize int
	Scene    string
	Search   string // 按素材名称/原始文件名模糊搜索
}

// AffiliateProfileStatsAggregate 推广用户统计聚合结果
type AffiliateProfileStatsAggregate struct {
	ClickCount          int64
	ValidOrderCount     int64
	PendingCommission   decimal.Decimal
	AvailableCommission decimal.Decimal
	WithdrawnCommission decimal.Decimal
}
