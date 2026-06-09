package repository

import (
	"errors"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/models"

	"gorm.io/gorm"
)

// PromotionRepository 活动价数据访问接口
type PromotionRepository interface {
	GetByID(id uint) (*models.Promotion, error)
	GetActiveByProduct(productID uint, now time.Time) (*models.Promotion, error)
	GetAllActiveByProduct(productID uint, now time.Time) ([]models.Promotion, error)
	Create(promotion *models.Promotion) error
	Update(promotion *models.Promotion) error
	Delete(id uint) error
	List(filter PromotionListFilter) ([]models.Promotion, int64, error)
	WithTx(tx *gorm.DB) *GormPromotionRepository
}

// PromotionListFilter 活动价列表筛选
type PromotionListFilter struct {
	ID         uint
	Name       string
	ScopeRefID uint
	IsActive   *bool
	Page       int
	PageSize   int
}

// GormPromotionRepository GORM 实现
type GormPromotionRepository struct {
	db *gorm.DB
}

// NewPromotionRepository 创建活动价仓库
func NewPromotionRepository(db *gorm.DB) *GormPromotionRepository {
	return &GormPromotionRepository{db: db}
}

// WithTx 绑定事务
func (r *GormPromotionRepository) WithTx(tx *gorm.DB) *GormPromotionRepository {
	if tx == nil {
		return r
	}
	return &GormPromotionRepository{db: tx}
}

// GetByID 根据ID获取活动价
func (r *GormPromotionRepository) GetByID(id uint) (*models.Promotion, error) {
	var promotion models.Promotion
	if err := r.db.First(&promotion, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &promotion, nil
}

// GetActiveByProduct 获取商品有效活动价
func (r *GormPromotionRepository) GetActiveByProduct(productID uint, now time.Time) (*models.Promotion, error) {
	var promotion models.Promotion
	query := r.db.Where("scope_type = ? AND scope_ref_id = ? AND is_active = ?", constants.ScopeTypeProduct, productID, true)
	query = query.Where("(starts_at IS NULL OR starts_at <= ?)", now)
	query = query.Where("(ends_at IS NULL OR ends_at >= ?)", now)
	if err := query.Order("id desc").First(&promotion).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &promotion, nil
}

// GetAllActiveByProduct 获取商品所有有效活动价（按 MinAmount 升序）
func (r *GormPromotionRepository) GetAllActiveByProduct(productID uint, now time.Time) ([]models.Promotion, error) {
	var promotions []models.Promotion
	query := r.db.Where("scope_type = ? AND scope_ref_id = ? AND is_active = ?", constants.ScopeTypeProduct, productID, true)
	query = query.Where("(starts_at IS NULL OR starts_at <= ?)", now)
	query = query.Where("(ends_at IS NULL OR ends_at >= ?)", now)
	if err := query.Order("min_amount asc").Find(&promotions).Error; err != nil {
		return nil, err
	}
	return promotions, nil
}

// Create 创建活动价
func (r *GormPromotionRepository) Create(promotion *models.Promotion) error {
	return r.db.Create(promotion).Error
}

// Update 更新活动价
func (r *GormPromotionRepository) Update(promotion *models.Promotion) error {
	return r.db.Save(promotion).Error
}

// Delete 删除活动价
func (r *GormPromotionRepository) Delete(id uint) error {
	return r.db.Delete(&models.Promotion{}, id).Error
}

// List 获取活动价列表
func (r *GormPromotionRepository) List(filter PromotionListFilter) ([]models.Promotion, int64, error) {
	var promotions []models.Promotion
	query := r.db.Model(&models.Promotion{})

	if filter.ID != 0 {
		query = query.Where("id = ?", filter.ID)
	}
	if name := strings.TrimSpace(filter.Name); name != "" {
		// LOWER(...) LIKE LOWER(?) 保证 SQLite 与 PostgreSQL 大小写不敏感行为一致
		query = query.Where("LOWER(name) LIKE LOWER(?)", "%"+name+"%")
	}
	if filter.ScopeRefID != 0 {
		query = query.Where("scope_ref_id = ?", filter.ScopeRefID)
	}
	if filter.IsActive != nil {
		query = query.Where("is_active = ?", *filter.IsActive)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = applyPagination(query, filter.Page, filter.PageSize)

	if err := query.Order("id desc").Find(&promotions).Error; err != nil {
		return nil, 0, err
	}
	return promotions, total, nil
}
