package admin

import (
	"errors"

	"github.com/dujiao-next/internal/http/handlers/shared"
	"github.com/dujiao-next/internal/http/response"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/dujiao-next/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// CreatePromotionRequest 创建活动价请求
type CreatePromotionRequest struct {
	Name       string  `json:"name" binding:"required"`
	Type       string  `json:"type" binding:"required"`
	ScopeRefID uint    `json:"scope_ref_id" binding:"required"`
	Value      float64 `json:"value" binding:"required"`
	MinAmount  float64 `json:"min_amount"`
	StartsAt   string  `json:"starts_at"`
	EndsAt     string  `json:"ends_at"`
	IsActive   *bool   `json:"is_active"`
}

func buildCreatePromotionInputFromRequest(req CreatePromotionRequest) (service.CreatePromotionInput, error) {
	startsAt, err := shared.ParseTimeNullable(req.StartsAt)
	if err != nil {
		return service.CreatePromotionInput{}, err
	}
	endsAt, err := shared.ParseTimeNullable(req.EndsAt)
	if err != nil {
		return service.CreatePromotionInput{}, err
	}
	return service.CreatePromotionInput{
		Name:       req.Name,
		Type:       req.Type,
		ScopeRefID: req.ScopeRefID,
		Value:      models.NewMoneyFromDecimal(decimal.NewFromFloat(req.Value)),
		MinAmount:  models.NewMoneyFromDecimal(decimal.NewFromFloat(req.MinAmount)),
		StartsAt:   startsAt,
		EndsAt:     endsAt,
		IsActive:   req.IsActive,
	}, nil
}

func buildUpdatePromotionInputFromRequest(req CreatePromotionRequest) (service.UpdatePromotionInput, error) {
	input, err := buildCreatePromotionInputFromRequest(req)
	if err != nil {
		return service.UpdatePromotionInput{}, err
	}
	return service.UpdatePromotionInput(input), nil
}

// CreatePromotion 创建活动价
func (h *Handler) CreatePromotion(c *gin.Context) {
	var req CreatePromotionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}

	input, err := buildCreatePromotionInputFromRequest(req)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	promotion, err := h.PromotionAdminService.Create(input)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrPromotionInvalid):
			shared.RespondError(c, response.CodeBadRequest, "error.promotion_invalid", nil)
		default:
			shared.RespondError(c, response.CodeInternal, "error.promotion_create_failed", err)
		}
		return
	}

	response.Success(c, promotion)
}

// UpdatePromotion 更新活动价
func (h *Handler) UpdatePromotion(c *gin.Context) {
	promotionID, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	var req CreatePromotionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondBindError(c, err)
		return
	}

	input, err := buildUpdatePromotionInputFromRequest(req)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	promotion, err := h.PromotionAdminService.Update(promotionID, input)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrPromotionNotFound):
			shared.RespondError(c, response.CodeNotFound, "error.promotion_not_found", nil)
		case errors.Is(err, service.ErrPromotionInvalid):
			shared.RespondError(c, response.CodeBadRequest, "error.promotion_invalid", nil)
		default:
			shared.RespondError(c, response.CodeInternal, "error.promotion_update_failed", err)
		}
		return
	}

	response.Success(c, promotion)
}

// DeletePromotion 删除活动价
func (h *Handler) DeletePromotion(c *gin.Context) {
	promotionID, err := shared.ParseParamUint(c, "id")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	if err := h.PromotionAdminService.Delete(promotionID); err != nil {
		switch {
		case errors.Is(err, service.ErrPromotionNotFound):
			shared.RespondError(c, response.CodeNotFound, "error.promotion_not_found", nil)
		case errors.Is(err, service.ErrPromotionInvalid):
			shared.RespondError(c, response.CodeBadRequest, "error.promotion_invalid", nil)
		default:
			shared.RespondError(c, response.CodeInternal, "error.promotion_delete_failed", err)
		}
		return
	}
	response.Success(c, gin.H{
		"deleted": true,
	})
}

// GetAdminPromotions 获取活动价列表
func (h *Handler) GetAdminPromotions(c *gin.Context) {
	page, pageSize := shared.ParsePagination(c)

	id, err := shared.ParseQueryUint(c.Query("id"), true)
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	scopeRefID, _ := shared.ParseQueryUint(c.Query("scope_ref_id"), false)

	isActive, err := shared.ParseQueryBoolPtr(c, "is_active")
	if err != nil {
		shared.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	promotions, total, err := h.PromotionAdminService.List(repository.PromotionListFilter{
		ID:         id,
		Name:       c.Query("name"),
		ScopeRefID: scopeRefID,
		IsActive:   isActive,
		Page:       page,
		PageSize:   pageSize,
	})
	if err != nil {
		shared.RespondError(c, response.CodeInternal, "error.promotion_fetch_failed", err)
		return
	}

	pagination := response.BuildPagination(page, pageSize, total)
	response.SuccessWithPage(c, promotions, pagination)
}
