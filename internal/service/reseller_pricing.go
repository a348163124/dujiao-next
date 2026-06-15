package service

import (
	"sort"
	"strings"
	"time"

	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"
	"github.com/shopspring/decimal"
)

const (
	resellerRuleSourceSKU     = "sku"
	resellerRuleSourceProduct = "product"
	resellerRuleSourceProfile = "profile"
	resellerRuleSourceInherit = "inherit"

	resellerProfitBlockOwner          = "self_dealing_owner"
	resellerProfitBlockRelatedAccount = "self_dealing_related_account"
)

// ResellerPricingResolver resolves reseller-facing prices before order transactions.
type ResellerPricingResolver struct {
	repo repository.ResellerRepository
}

func NewResellerPricingResolver(repo repository.ResellerRepository) *ResellerPricingResolver {
	return &ResellerPricingResolver{repo: repo}
}

type ResellerOrderPricingContext struct {
	ResellerID        uint
	Domain            string
	Currency          string
	ResellerUserID    uint
	BuyerUserID       uint
	BaseAmount        decimal.Decimal
	ResellerAmount    decimal.Decimal
	ProfitAmount      decimal.Decimal
	EffectiveProfit   decimal.Decimal
	ProfitEligible    bool
	ProfitBlockReason string
	Items             []ResellerOrderPricingItem
	PricingSnapshot   models.JSON
	RiskSnapshot      models.JSON
}

type ResellerOrderPricingItem struct {
	ProductID           uint
	SKUID               uint
	Quantity            int
	ChildOrderID        uint
	BaseUnitAmount      decimal.Decimal
	ResellerUnitAmount  decimal.Decimal
	BaseTotalAmount     decimal.Decimal
	ResellerTotalAmount decimal.Decimal
	ProfitAmount        decimal.Decimal
	PricingMode         string
	RuleSource          string
	SettingID           *uint
	OrderID             uint
	OrderItemID         uint
}

func (ctx *ResellerOrderPricingContext) BindCreatedOrderItem(index int, childOrderID uint, orderItemID uint) {
	if ctx == nil || index < 0 || index >= len(ctx.Items) {
		return
	}
	ctx.Items[index].ChildOrderID = childOrderID
	ctx.Items[index].OrderID = childOrderID
	ctx.Items[index].OrderItemID = orderItemID
	ctx.PricingSnapshot = ctx.buildPricingSnapshotJSON()
}

func (ctx *ResellerOrderPricingContext) BuildSnapshot(orderID uint, now time.Time) *models.ResellerOrderSnapshot {
	if ctx == nil {
		return nil
	}
	for i := range ctx.Items {
		if ctx.Items[i].OrderID == 0 {
			ctx.Items[i].OrderID = ctx.Items[i].ChildOrderID
		}
	}
	ctx.PricingSnapshot = ctx.buildPricingSnapshotJSON()
	return &models.ResellerOrderSnapshot{
		OrderID:             orderID,
		ResellerID:          ctx.ResellerID,
		Domain:              ctx.Domain,
		Currency:            ctx.Currency,
		ResellerUserID:      ctx.ResellerUserID,
		BuyerUserID:         ctx.BuyerUserID,
		BaseAmount:          models.NewMoneyFromDecimal(ctx.BaseAmount),
		ResellerAmount:      models.NewMoneyFromDecimal(ctx.ResellerAmount),
		ProfitAmount:        models.NewMoneyFromDecimal(ctx.ProfitAmount),
		ProfitEligible:      ctx.ProfitEligible,
		ProfitBlockReason:   ctx.ProfitBlockReason,
		PricingSnapshotJSON: ctx.PricingSnapshot,
		RiskSnapshotJSON:    ctx.RiskSnapshot,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
}

type ResellerDisplayPriceResult struct {
	Visible      bool
	ProductID    uint
	DisplaySKUID uint
	DisplayPrice models.Money
	SKUPrices    map[uint]models.Money
	HiddenSKUIDs map[uint]bool
}

type ResellerDisplayPricingBatch struct {
	Tenant            TenantContext
	Profile           *models.ResellerProfile
	SettingsByProduct map[uint][]models.ResellerProductSetting
}

type resellerPricingRule struct {
	Mode              string
	Source            string
	SettingID         *uint
	MarkupPercent     decimal.Decimal
	FixedMarkupAmount decimal.Decimal
	FixedPriceAmount  decimal.Decimal
}

func (r *ResellerPricingResolver) ApplyToOrderBuildResult(tenant TenantContext, buyerUserID uint, result *orderBuildResult) (*ResellerOrderPricingContext, error) {
	if !isResellerOrderContext(tenant) {
		return nil, nil
	}
	if r == nil || r.repo == nil || result == nil || tenant.ResellerID == nil {
		return nil, ErrResellerProductNotListed
	}
	profile, err := r.loadActiveProfile(*tenant.ResellerID)
	if err != nil {
		return nil, err
	}
	productIDs, skuIDs := collectOrderPlanIDs(result.Plans)
	settings, err := r.repo.ListProductSettingsForPricing(*tenant.ResellerID, productIDs, skuIDs)
	if err != nil {
		return nil, err
	}
	settingsByProduct, settingsBySKU := buildSettingIndexes(settings)

	ctx := &ResellerOrderPricingContext{
		ResellerID:     *tenant.ResellerID,
		Domain:         resellerSnapshotDomain(tenant),
		Currency:       result.Currency,
		ResellerUserID: profile.UserID,
		BuyerUserID:    buyerUserID,
		ProfitEligible: true,
		Items:          make([]ResellerOrderPricingItem, 0, len(result.Plans)),
	}

	for i := range result.Plans {
		plan := &result.Plans[i]
		if plan == nil || plan.Product == nil || plan.SKU == nil {
			return nil, ErrProductSKUInvalid
		}
		productSetting := settingsByProduct[plan.Product.ID]
		skuSetting := settingsBySKU[resellerSettingKey{productID: plan.Product.ID, skuID: plan.SKU.ID}]
		if productSetting != nil && !productSetting.IsListed {
			return nil, ErrResellerProductNotListed
		}
		if skuSetting != nil && !skuSetting.IsListed {
			return nil, ErrResellerProductNotListed
		}

		baseUnit := plan.SKU.PriceAmount.Decimal.Round(2)
		resellerUnit, rule, err := resolveResellerUnitAmount(profile, productSetting, skuSetting, baseUnit)
		if err != nil {
			return nil, err
		}
		if err := validateResellerUnitAmount(profile, plan.SKU, baseUnit, resellerUnit); err != nil {
			return nil, err
		}
		quantity := decimal.NewFromInt(int64(plan.Item.Quantity))
		baseTotal := baseUnit.Mul(quantity).Round(2)
		resellerTotal := resellerUnit.Mul(quantity).Round(2)
		profit := resellerTotal.Sub(baseTotal).Round(2)

		zeroMoney := models.NewMoneyFromDecimal(decimal.Zero)
		plan.TotalAmount = resellerTotal
		plan.CouponDiscount = decimal.Zero
		plan.MemberDiscount = decimal.Zero
		plan.PromotionDiscount = decimal.Zero
		plan.WholesaleDiscount = decimal.Zero
		plan.Item.OriginalUnitPrice = models.NewMoneyFromDecimal(resellerUnit)
		plan.Item.UnitPrice = models.NewMoneyFromDecimal(resellerUnit)
		plan.Item.OriginalTotalPrice = models.NewMoneyFromDecimal(resellerTotal)
		plan.Item.TotalPrice = models.NewMoneyFromDecimal(resellerTotal)
		plan.Item.MemberDiscount = zeroMoney
		plan.Item.CouponDiscount = zeroMoney
		plan.Item.PromotionDiscount = zeroMoney
		plan.Item.WholesaleDiscount = zeroMoney
		plan.Item.PromotionID = nil

		ctx.BaseAmount = ctx.BaseAmount.Add(baseTotal).Round(2)
		ctx.ResellerAmount = ctx.ResellerAmount.Add(resellerTotal).Round(2)
		ctx.ProfitAmount = ctx.ProfitAmount.Add(profit).Round(2)
		ctx.Items = append(ctx.Items, ResellerOrderPricingItem{
			ProductID:           plan.Product.ID,
			SKUID:               plan.SKU.ID,
			Quantity:            plan.Item.Quantity,
			BaseUnitAmount:      baseUnit,
			ResellerUnitAmount:  resellerUnit,
			BaseTotalAmount:     baseTotal,
			ResellerTotalAmount: resellerTotal,
			ProfitAmount:        profit,
			PricingMode:         rule.Mode,
			RuleSource:          rule.Source,
			SettingID:           rule.SettingID,
		})
	}

	result.OriginalAmount = decimal.Zero
	result.TotalAmount = decimal.Zero
	for _, plan := range result.Plans {
		result.OriginalAmount = result.OriginalAmount.Add(plan.TotalAmount).Round(2)
		result.TotalAmount = result.TotalAmount.Add(plan.TotalAmount.Sub(plan.CouponDiscount)).Round(2)
	}
	result.DiscountAmount = decimal.Zero
	result.MemberDiscountAmount = decimal.Zero
	result.PromotionDiscountAmount = decimal.Zero
	result.WholesaleDiscountAmount = decimal.Zero
	result.AppliedCoupon = nil
	result.OrderPromotionID = nil
	result.MemberLevelID = nil

	if err := r.applySelfDealingRisk(ctx, profile); err != nil {
		return nil, err
	}
	if ctx.ProfitEligible {
		ctx.EffectiveProfit = ctx.ProfitAmount
	} else {
		ctx.EffectiveProfit = decimal.Zero
	}
	ctx.PricingSnapshot = ctx.buildPricingSnapshotJSON()
	ctx.RiskSnapshot = ctx.buildRiskSnapshotJSON()
	return ctx, nil
}

func (r *ResellerPricingResolver) LoadDisplayPricingBatch(tenant TenantContext, products []models.Product) (*ResellerDisplayPricingBatch, error) {
	if !isResellerOrderContext(tenant) {
		return nil, nil
	}
	if r == nil || r.repo == nil || tenant.ResellerID == nil {
		return nil, ErrResellerProductNotListed
	}
	profile, err := r.loadActiveProfile(*tenant.ResellerID)
	if err != nil {
		return nil, err
	}
	productIDs, skuIDs := collectProductIDs(products)
	settings, err := r.repo.ListProductSettingsForPricing(*tenant.ResellerID, productIDs, skuIDs)
	if err != nil {
		return nil, err
	}
	byProduct := make(map[uint][]models.ResellerProductSetting)
	for _, setting := range settings {
		byProduct[setting.ProductID] = append(byProduct[setting.ProductID], setting)
	}
	return &ResellerDisplayPricingBatch{
		Tenant:            tenant,
		Profile:           profile,
		SettingsByProduct: byProduct,
	}, nil
}

func (r *ResellerPricingResolver) ResolveDisplayPrices(tenant TenantContext, product *models.Product, batch *ResellerDisplayPricingBatch) (*ResellerDisplayPriceResult, error) {
	if !isResellerOrderContext(tenant) {
		return nil, nil
	}
	if product == nil || batch == nil || batch.Profile == nil {
		return nil, ErrResellerProductNotListed
	}
	productSettings, skuSettings := buildSettingIndexes(batch.SettingsByProduct[product.ID])
	productSetting := productSettings[product.ID]
	if productSetting != nil && !productSetting.IsListed {
		return &ResellerDisplayPriceResult{Visible: false, ProductID: product.ID}, nil
	}

	result := &ResellerDisplayPriceResult{
		Visible:      false,
		ProductID:    product.ID,
		SKUPrices:    map[uint]models.Money{},
		HiddenSKUIDs: map[uint]bool{},
	}
	for _, sku := range product.SKUs {
		if !sku.IsActive {
			continue
		}
		skuSetting := skuSettings[resellerSettingKey{productID: product.ID, skuID: sku.ID}]
		if skuSetting != nil && !skuSetting.IsListed {
			result.HiddenSKUIDs[sku.ID] = true
			continue
		}
		price, _, err := resolveResellerUnitAmount(batch.Profile, productSetting, skuSetting, sku.PriceAmount.Decimal.Round(2))
		if err != nil {
			return nil, err
		}
		if err := validateResellerUnitAmount(batch.Profile, &sku, sku.PriceAmount.Decimal.Round(2), price); err != nil {
			return nil, err
		}
		money := models.NewMoneyFromDecimal(price)
		result.SKUPrices[sku.ID] = money
		if !result.Visible {
			result.Visible = true
			result.DisplaySKUID = sku.ID
			result.DisplayPrice = money
		}
	}
	if len(product.SKUs) == 0 {
		price, _, err := resolveResellerUnitAmount(batch.Profile, productSetting, nil, product.PriceAmount.Decimal.Round(2))
		if err != nil {
			return nil, err
		}
		result.Visible = true
		result.DisplayPrice = models.NewMoneyFromDecimal(price)
	}
	return result, nil
}

func (r *ResellerPricingResolver) loadActiveProfile(resellerID uint) (*models.ResellerProfile, error) {
	profile, err := r.repo.GetProfileByID(resellerID)
	if err != nil {
		return nil, err
	}
	if profile == nil || profile.Status != models.ResellerProfileStatusActive {
		return nil, ErrResellerProductNotListed
	}
	return profile, nil
}

func (r *ResellerPricingResolver) applySelfDealingRisk(ctx *ResellerOrderPricingContext, profile *models.ResellerProfile) error {
	if ctx == nil || profile == nil {
		return nil
	}
	ownerMatch := false
	relatedMatch := false
	if ctx.BuyerUserID > 0 && ctx.BuyerUserID == profile.UserID {
		ownerMatch = true
		ctx.ProfitEligible = false
		ctx.ProfitBlockReason = resellerProfitBlockOwner
	} else if ctx.BuyerUserID > 0 {
		matched, err := r.repo.IsActiveRelatedAccount(ctx.ResellerID, ctx.BuyerUserID)
		if err != nil {
			return err
		}
		if matched {
			relatedMatch = true
			ctx.ProfitEligible = false
			ctx.ProfitBlockReason = resellerProfitBlockRelatedAccount
		}
	}
	ctx.RiskSnapshot = models.JSON{
		"buyer_user_id":         ctx.BuyerUserID,
		"reseller_user_id":      ctx.ResellerUserID,
		"profit_eligible":       ctx.ProfitEligible,
		"profit_block_reason":   ctx.ProfitBlockReason,
		"guest_buyer":           ctx.BuyerUserID == 0,
		"self_dealing_deferred": "same_contact_and_risk_detected_account_linking",
		"self_dealing": models.JSON{
			"owner_match":           ownerMatch,
			"related_account_match": relatedMatch,
		},
	}
	return nil
}

type resellerSettingKey struct {
	productID uint
	skuID     uint
}

func buildSettingIndexes(settings []models.ResellerProductSetting) (map[uint]*models.ResellerProductSetting, map[resellerSettingKey]*models.ResellerProductSetting) {
	byProduct := make(map[uint]*models.ResellerProductSetting)
	bySKU := make(map[resellerSettingKey]*models.ResellerProductSetting)
	for i := range settings {
		setting := settings[i]
		if setting.ProductID == 0 {
			continue
		}
		row := setting
		if setting.SKUID == 0 {
			byProduct[setting.ProductID] = &row
			continue
		}
		bySKU[resellerSettingKey{productID: setting.ProductID, skuID: setting.SKUID}] = &row
	}
	return byProduct, bySKU
}

func resolveResellerUnitAmount(profile *models.ResellerProfile, productSetting *models.ResellerProductSetting, skuSetting *models.ResellerProductSetting, baseUnit decimal.Decimal) (decimal.Decimal, resellerPricingRule, error) {
	if skuSetting != nil && strings.TrimSpace(skuSetting.PricingMode) != models.ResellerPricingModeInherit {
		return applyResellerPricingRule(*skuSetting, resellerRuleSourceSKU, baseUnit)
	}
	if productSetting != nil && strings.TrimSpace(productSetting.PricingMode) != models.ResellerPricingModeInherit {
		return applyResellerPricingRule(*productSetting, resellerRuleSourceProduct, baseUnit)
	}
	if profile != nil && profile.DefaultMarkupPercent.Decimal.GreaterThan(decimal.Zero) {
		rule := resellerPricingRule{
			Mode:          models.ResellerPricingModeMarkupPercent,
			Source:        resellerRuleSourceProfile,
			MarkupPercent: profile.DefaultMarkupPercent.Decimal.Round(2),
		}
		unit := applyMarkupPercent(baseUnit, rule.MarkupPercent)
		return unit, rule, nil
	}
	return baseUnit.Round(2), resellerPricingRule{Mode: models.ResellerPricingModeInherit, Source: resellerRuleSourceInherit}, nil
}

func applyResellerPricingRule(setting models.ResellerProductSetting, source string, baseUnit decimal.Decimal) (decimal.Decimal, resellerPricingRule, error) {
	settingID := setting.ID
	rule := resellerPricingRule{
		Mode:              strings.TrimSpace(setting.PricingMode),
		Source:            source,
		SettingID:         &settingID,
		MarkupPercent:     setting.MarkupPercent.Decimal.Round(2),
		FixedMarkupAmount: setting.FixedMarkupAmount.Decimal.Round(2),
		FixedPriceAmount:  setting.FixedPriceAmount.Decimal.Round(2),
	}
	switch rule.Mode {
	case models.ResellerPricingModeMarkupPercent:
		return applyMarkupPercent(baseUnit, rule.MarkupPercent), rule, nil
	case models.ResellerPricingModeFixedMarkup:
		return baseUnit.Add(rule.FixedMarkupAmount).Round(2), rule, nil
	case models.ResellerPricingModeFixedPrice:
		return rule.FixedPriceAmount.Round(2), rule, nil
	case models.ResellerPricingModeInherit:
		return baseUnit.Round(2), rule, nil
	default:
		return decimal.Zero, rule, ErrResellerPricingModeInvalid
	}
}

func applyMarkupPercent(baseUnit decimal.Decimal, percent decimal.Decimal) decimal.Decimal {
	return baseUnit.Mul(decimal.NewFromInt(100).Add(percent)).Div(decimal.NewFromInt(100)).Round(2)
}

func validateResellerUnitAmount(profile *models.ResellerProfile, sku *models.ProductSKU, baseUnit decimal.Decimal, resellerUnit decimal.Decimal) error {
	baseUnit = baseUnit.Round(2)
	resellerUnit = resellerUnit.Round(2)
	if resellerUnit.LessThanOrEqual(decimal.Zero) || resellerUnit.LessThan(baseUnit) {
		return ErrResellerPriceBelowBase
	}
	if sku != nil && sku.CostPriceAmount.Decimal.GreaterThan(decimal.Zero) && resellerUnit.LessThan(sku.CostPriceAmount.Decimal.Round(2)) {
		return ErrResellerPriceBelowBase
	}
	if profile != nil && profile.MaxMarkupPercent.Decimal.GreaterThan(decimal.Zero) && baseUnit.GreaterThan(decimal.Zero) {
		implicit := resellerUnit.Sub(baseUnit).Div(baseUnit).Mul(decimal.NewFromInt(100)).Round(4)
		if implicit.GreaterThan(profile.MaxMarkupPercent.Decimal.Round(4)) {
			return ErrResellerMarkupExceeded
		}
	}
	return nil
}

func collectOrderPlanIDs(plans []childOrderPlan) ([]uint, []uint) {
	productIDs := make([]uint, 0, len(plans))
	skuIDs := make([]uint, 0, len(plans))
	for _, plan := range plans {
		if plan.Product != nil {
			productIDs = append(productIDs, plan.Product.ID)
		} else if plan.Item.ProductID > 0 {
			productIDs = append(productIDs, plan.Item.ProductID)
		}
		if plan.SKU != nil {
			skuIDs = append(skuIDs, plan.SKU.ID)
		} else if plan.Item.SKUID > 0 {
			skuIDs = append(skuIDs, plan.Item.SKUID)
		}
	}
	return uniqueServiceUintSlice(productIDs), uniqueServiceUintSlice(skuIDs)
}

func collectProductIDs(products []models.Product) ([]uint, []uint) {
	productIDs := make([]uint, 0, len(products))
	skuIDs := []uint{}
	for _, product := range products {
		if product.ID > 0 {
			productIDs = append(productIDs, product.ID)
		}
		for _, sku := range product.SKUs {
			if sku.ID > 0 {
				skuIDs = append(skuIDs, sku.ID)
			}
		}
	}
	return uniqueServiceUintSlice(productIDs), uniqueServiceUintSlice(skuIDs)
}

func uniqueServiceUintSlice(values []uint) []uint {
	if len(values) == 0 {
		return nil
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	result := make([]uint, 0, len(values))
	var last uint
	for i, value := range values {
		if value == 0 {
			continue
		}
		if i > 0 && value == last {
			continue
		}
		result = append(result, value)
		last = value
	}
	return result
}

func isResellerOrderContext(tenant TenantContext) bool {
	return tenant.ResellerID != nil && !tenant.IsMain && !tenant.Unavailable
}

func resellerSnapshotDomain(tenant TenantContext) string {
	if host := strings.TrimSpace(tenant.PrimaryDomain); host != "" {
		return NormalizeResellerHost(host)
	}
	return NormalizeResellerHost(tenant.Host)
}

func (ctx *ResellerOrderPricingContext) buildPricingSnapshotJSON() models.JSON {
	items := make([]interface{}, 0, len(ctx.Items))
	for _, item := range ctx.Items {
		entry := models.JSON{
			"product_id":            item.ProductID,
			"sku_id":                item.SKUID,
			"quantity":              item.Quantity,
			"child_order_id":        item.ChildOrderID,
			"base_unit_amount":      moneyString(item.BaseUnitAmount),
			"reseller_unit_amount":  moneyString(item.ResellerUnitAmount),
			"base_total_amount":     moneyString(item.BaseTotalAmount),
			"reseller_total_amount": moneyString(item.ResellerTotalAmount),
			"profit_amount":         moneyString(item.ProfitAmount),
			"pricing_mode":          item.PricingMode,
			"rule_source":           item.RuleSource,
			"order_id":              item.OrderID,
			"order_item_id":         item.OrderItemID,
		}
		if item.SettingID != nil {
			entry["setting_id"] = *item.SettingID
		} else {
			entry["setting_id"] = nil
		}
		items = append(items, entry)
	}
	return models.JSON{
		"currency":        ctx.Currency,
		"base_amount":     moneyString(ctx.BaseAmount),
		"reseller_amount": moneyString(ctx.ResellerAmount),
		"profit_amount":   moneyString(ctx.ProfitAmount),
		"items":           items,
	}
}

func (ctx *ResellerOrderPricingContext) buildRiskSnapshotJSON() models.JSON {
	if ctx.RiskSnapshot != nil {
		return ctx.RiskSnapshot
	}
	return models.JSON{
		"buyer_user_id":       ctx.BuyerUserID,
		"reseller_user_id":    ctx.ResellerUserID,
		"profit_eligible":     ctx.ProfitEligible,
		"profit_block_reason": ctx.ProfitBlockReason,
	}
}

func moneyString(value decimal.Decimal) string {
	return value.Round(2).StringFixed(2)
}
