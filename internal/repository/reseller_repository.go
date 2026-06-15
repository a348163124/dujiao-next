package repository

import (
	"errors"
	"strings"
	"time"

	"github.com/dujiao-next/internal/models"
	"gorm.io/gorm"
)

// ResellerRepository 分销商数据访问接口。
type ResellerRepository interface {
	Transaction(fn func(tx *gorm.DB) error) error
	WithTx(tx *gorm.DB) ResellerRepository
	CreateProfile(profile *models.ResellerProfile) error
	GetProfileByID(id uint) (*models.ResellerProfile, error)
	GetProfileByUserID(userID uint) (*models.ResellerProfile, error)
	UpsertDomain(domain models.ResellerDomain) (*models.ResellerDomain, error)
	FindDomainByHost(host string) (*models.ResellerDomain, error)
	FindActiveVerifiedDomain(host string) (*models.ResellerDomain, error)
	UpsertSiteConfig(config models.ResellerSiteConfig) (*models.ResellerSiteConfig, error)
	ListProductSettingsForPricing(resellerID uint, productIDs []uint, skuIDs []uint) ([]models.ResellerProductSetting, error)
	ListHiddenProductIDs(resellerID uint) ([]uint, error)
	IsActiveRelatedAccount(resellerID uint, userID uint) (bool, error)
	CreateOrderSnapshot(snapshot *models.ResellerOrderSnapshot) error
	GetOrderSnapshotByOrderID(orderID uint) (*models.ResellerOrderSnapshot, error)
}

// GormResellerRepository GORM 分销商仓储。
type GormResellerRepository struct {
	BaseRepository
}

// NewResellerRepository 创建分销商仓储。
func NewResellerRepository(db *gorm.DB) *GormResellerRepository {
	return &GormResellerRepository{BaseRepository: BaseRepository{db: db}}
}

// WithTx 绑定事务。
func (r *GormResellerRepository) WithTx(tx *gorm.DB) ResellerRepository {
	if tx == nil {
		return r
	}
	return &GormResellerRepository{BaseRepository: BaseRepository{db: tx}}
}

// CreateProfile 创建分销商资料。
func (r *GormResellerRepository) CreateProfile(profile *models.ResellerProfile) error {
	if profile == nil {
		return errors.New("reseller profile is nil")
	}
	return r.db.Create(profile).Error
}

// GetProfileByID 按 ID 获取分销商资料。
func (r *GormResellerRepository) GetProfileByID(id uint) (*models.ResellerProfile, error) {
	if id == 0 {
		return nil, nil
	}
	var profile models.ResellerProfile
	if err := r.db.Preload("User").First(&profile, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &profile, nil
}

// GetProfileByUserID 按用户 ID 获取分销商资料。
func (r *GormResellerRepository) GetProfileByUserID(userID uint) (*models.ResellerProfile, error) {
	if userID == 0 {
		return nil, nil
	}
	var profile models.ResellerProfile
	if err := r.db.Preload("User").Where("user_id = ?", userID).First(&profile).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &profile, nil
}

// UpsertDomain 创建域名，或恢复同域名的软删除记录。
func (r *GormResellerRepository) UpsertDomain(input models.ResellerDomain) (*models.ResellerDomain, error) {
	input.Domain = normalizeDomainForRepository(input.Domain)
	if input.ResellerID == 0 || input.Domain == "" {
		return nil, errors.New("invalid reseller domain")
	}
	now := time.Now()
	var existing models.ResellerDomain
	err := r.db.Unscoped().Where("domain = ?", input.Domain).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		input.CreatedAt = now
		input.UpdatedAt = now
		if err := r.db.Create(&input).Error; err != nil {
			return nil, err
		}
		return &input, nil
	}
	if !existing.DeletedAt.Valid {
		return nil, errors.New("reseller domain already exists")
	}
	existing.ResellerID = input.ResellerID
	existing.Type = input.Type
	existing.VerificationToken = input.VerificationToken
	existing.VerificationStatus = input.VerificationStatus
	existing.Status = input.Status
	existing.IsPrimary = input.IsPrimary
	existing.VerifiedAt = input.VerifiedAt
	existing.DeletedAt = gorm.DeletedAt{}
	existing.UpdatedAt = now
	if err := r.db.Unscoped().Save(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}

// FindDomainByHost 按域名获取未删除域名记录。
func (r *GormResellerRepository) FindDomainByHost(host string) (*models.ResellerDomain, error) {
	domain := normalizeDomainForRepository(host)
	if domain == "" {
		return nil, nil
	}
	var row models.ResellerDomain
	err := r.db.Preload("Profile").Where("domain = ?", domain).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// FindActiveVerifiedDomain 按域名获取已验证且启用的分销域名。
func (r *GormResellerRepository) FindActiveVerifiedDomain(host string) (*models.ResellerDomain, error) {
	domain := normalizeDomainForRepository(host)
	if domain == "" {
		return nil, nil
	}
	var row models.ResellerDomain
	err := r.db.Preload("Profile").
		Where("domain = ? AND status = ? AND verification_status = ?", domain, models.ResellerDomainStatusActive, models.ResellerDomainVerificationVerified).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// UpsertSiteConfig 创建或恢复分销站点配置。
func (r *GormResellerRepository) UpsertSiteConfig(input models.ResellerSiteConfig) (*models.ResellerSiteConfig, error) {
	if input.ResellerID == 0 {
		return nil, errors.New("invalid reseller site config")
	}
	now := time.Now()
	var existing models.ResellerSiteConfig
	err := r.db.Unscoped().Where("reseller_id = ?", input.ResellerID).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		input.CreatedAt = now
		input.UpdatedAt = now
		if err := r.db.Create(&input).Error; err != nil {
			return nil, err
		}
		return &input, nil
	}
	existing.SiteName = input.SiteName
	existing.Logo = input.Logo
	existing.Favicon = input.Favicon
	existing.AnnouncementJSON = input.AnnouncementJSON
	existing.SupportJSON = input.SupportJSON
	existing.SEOJSON = input.SEOJSON
	existing.FooterLinksJSON = input.FooterLinksJSON
	existing.NavConfigJSON = input.NavConfigJSON
	existing.ThemeJSON = input.ThemeJSON
	existing.DeletedAt = gorm.DeletedAt{}
	existing.UpdatedAt = now
	if err := r.db.Unscoped().Save(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}

// ListProductSettingsForPricing 批量获取分销定价所需的商品级与 SKU 级配置。
func (r *GormResellerRepository) ListProductSettingsForPricing(resellerID uint, productIDs []uint, skuIDs []uint) ([]models.ResellerProductSetting, error) {
	if resellerID == 0 || len(productIDs) == 0 {
		return []models.ResellerProductSetting{}, nil
	}
	productIDs = uniqueUintSlice(productIDs)
	skuIDs = uniqueUintSlice(skuIDs)

	query := r.db.Where("reseller_id = ? AND product_id IN ?", resellerID, productIDs)
	if len(skuIDs) > 0 {
		query = query.Where("(sku_id = 0 OR sku_id IN ?)", skuIDs)
	} else {
		query = query.Where("sku_id = 0")
	}

	var rows []models.ResellerProductSetting
	if err := query.Order("product_id ASC, sku_id ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListHiddenProductIDs 返回分销前台列表应在查询层排除的商品 ID。
func (r *GormResellerRepository) ListHiddenProductIDs(resellerID uint) ([]uint, error) {
	if resellerID == 0 {
		return []uint{}, nil
	}

	hidden := map[uint]struct{}{}
	var productHidden []uint
	if err := r.db.Model(&models.ResellerProductSetting{}).
		Where("reseller_id = ? AND sku_id = 0 AND is_listed = ?", resellerID, false).
		Pluck("product_id", &productHidden).Error; err != nil {
		return nil, err
	}
	for _, id := range productHidden {
		if id != 0 {
			hidden[id] = struct{}{}
		}
	}

	var skuHidden []uint
	if err := r.db.Model(&models.ProductSKU{}).
		Select("product_skus.product_id").
		Joins(
			"JOIN reseller_product_settings rps ON rps.product_id = product_skus.product_id AND rps.sku_id = product_skus.id AND rps.reseller_id = ? AND rps.is_listed = ? AND rps.deleted_at IS NULL",
			resellerID,
			false,
		).
		Where("product_skus.is_active = ? AND product_skus.deleted_at IS NULL", true).
		Group("product_skus.product_id").
		Having("COUNT(product_skus.id) = (SELECT COUNT(1) FROM product_skus ps2 WHERE ps2.product_id = product_skus.product_id AND ps2.is_active = ? AND ps2.deleted_at IS NULL)", true).
		Pluck("product_skus.product_id", &skuHidden).Error; err != nil {
		return nil, err
	}
	for _, id := range skuHidden {
		if id != 0 {
			hidden[id] = struct{}{}
		}
	}

	ids := make([]uint, 0, len(hidden))
	for id := range hidden {
		ids = append(ids, id)
	}
	return ids, nil
}

// IsActiveRelatedAccount 判断用户是否为分销商已启用的关联账号。
func (r *GormResellerRepository) IsActiveRelatedAccount(resellerID uint, userID uint) (bool, error) {
	if resellerID == 0 || userID == 0 {
		return false, nil
	}
	var count int64
	if err := r.db.Model(&models.ResellerRelatedAccount{}).
		Where("reseller_id = ? AND user_id = ? AND status = ?", resellerID, userID, models.ResellerRelatedAccountStatusActive).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateOrderSnapshot 创建订单分销快照。
func (r *GormResellerRepository) CreateOrderSnapshot(snapshot *models.ResellerOrderSnapshot) error {
	if snapshot == nil || snapshot.OrderID == 0 || snapshot.ResellerID == 0 {
		return errors.New("invalid reseller order snapshot")
	}
	profitEligible := snapshot.ProfitEligible
	if err := r.db.Create(snapshot).Error; err != nil {
		return err
	}
	if !profitEligible {
		if err := r.db.Model(&models.ResellerOrderSnapshot{}).
			Where("id = ?", snapshot.ID).
			Update("profit_eligible", false).Error; err != nil {
			return err
		}
		snapshot.ProfitEligible = false
	}
	return nil
}

// GetOrderSnapshotByOrderID 按订单 ID 获取订单分销快照。
func (r *GormResellerRepository) GetOrderSnapshotByOrderID(orderID uint) (*models.ResellerOrderSnapshot, error) {
	if orderID == 0 {
		return nil, nil
	}
	var snapshot models.ResellerOrderSnapshot
	if err := r.db.Where("order_id = ?", orderID).First(&snapshot).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &snapshot, nil
}

func normalizeDomainForRepository(raw string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
}

func uniqueUintSlice(values []uint) []uint {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[uint]struct{}, len(values))
	result := make([]uint, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
