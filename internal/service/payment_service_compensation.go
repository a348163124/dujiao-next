package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/payment/provider"
)

const (
	paymentCompensationDelay = 2 * time.Minute
	paymentCompensationLimit = 50
)

type PaymentCompensationResult struct {
	Checked int
	Updated int
	Skipped int
	Failed  int
}

func (s *PaymentService) CompensateHuifuPayments(ctx context.Context) (*PaymentCompensationResult, error) {
	return s.CompensateProviderPayments(ctx, constants.PaymentProviderHuifu, paymentCompensationDelay, paymentCompensationLimit)
}

func (s *PaymentService) CompensateProviderPayments(ctx context.Context, providerType string, delay time.Duration, limit int) (*PaymentCompensationResult, error) {
	result := &PaymentCompensationResult{}
	providerType = strings.ToLower(strings.TrimSpace(providerType))
	if providerType == "" {
		return result, ErrPaymentProviderNotSupported
	}
	if delay <= 0 {
		delay = paymentCompensationDelay
	}
	if limit <= 0 || limit > paymentCompensationLimit {
		limit = paymentCompensationLimit
	}
	before := time.Now().Add(-delay)
	payments, err := s.paymentRepo.ListPendingByProvider(providerType, before, limit)
	if err != nil {
		return result, ErrPaymentUpdateFailed
	}
	for i := range payments {
		payment := payments[i]
		if strings.TrimSpace(payment.GatewayOrderNo) == "" {
			result.Skipped++
			continue
		}
		result.Checked++
		updated, err := s.compensatePayment(ctx, &payment)
		if err != nil {
			result.Failed++
			logger.Warnw("payment_compensation_failed",
				"payment_id", payment.ID,
				"provider_type", payment.ProviderType,
				"gateway_order_no", payment.GatewayOrderNo,
				"error", err,
			)
			continue
		}
		if updated != nil && updated.Status != constants.PaymentStatusPending && updated.Status != constants.PaymentStatusInitiated && updated.Status != constants.PaymentStatusExpired {
			result.Updated++
			logger.Infow("payment_compensation_updated",
				"payment_id", payment.ID,
				"provider_type", payment.ProviderType,
				"gateway_order_no", payment.GatewayOrderNo,
				"status", updated.Status,
			)
			continue
		}
		result.Skipped++
		if updated != nil {
			logger.Infow("payment_compensation_skipped",
				"payment_id", payment.ID,
				"provider_type", payment.ProviderType,
				"gateway_order_no", payment.GatewayOrderNo,
				"status", updated.Status,
			)
		}
	}
	logger.Infow("payment_compensation_finished",
		"provider_type", providerType,
		"checked", result.Checked,
		"updated", result.Updated,
		"skipped", result.Skipped,
		"failed", result.Failed,
	)
	return result, nil
}

func (s *PaymentService) compensatePayment(ctx context.Context, payment *models.Payment) (*models.Payment, error) {
	if payment == nil || payment.ID == 0 {
		return nil, ErrPaymentInvalid
	}
	channel, err := s.channelRepo.GetByID(payment.ChannelID)
	if err != nil {
		return nil, ErrPaymentUpdateFailed
	}
	if channel == nil {
		return nil, ErrPaymentChannelNotFound
	}
	return s.queryPaymentViaRegistry(ctx, payment, channel, payment.GatewayOrderNo)
}

func (s *PaymentService) queryPaymentViaRegistry(ctx context.Context, payment *models.Payment, channel *models.PaymentChannel, queryRef string) (*models.Payment, error) {
	if s.paymentProviderRegistry == nil {
		return nil, ErrPaymentProviderNotSupported
	}
	p, ok := s.paymentProviderRegistry.Lookup(channel.ProviderType, channel.ChannelType)
	if !ok {
		return nil, ErrPaymentProviderNotSupported
	}
	capturer, ok := p.(provider.Capturer)
	if !ok {
		return nil, ErrPaymentProviderNotSupported
	}
	if err := capturer.ValidateConfig(channel.ConfigJSON, channel.InteractionMode); err != nil {
		return nil, mapProviderErrorToService(err)
	}
	queryRef = strings.TrimSpace(queryRef)
	if queryRef == "" {
		return nil, ErrPaymentInvalid
	}
	queryCtx, cancel := detachOutboundRequestContext(ctx)
	defer cancel()
	queryResult, err := capturer.QueryPayment(queryCtx, channel.ConfigJSON, queryRef)
	if err != nil {
		return nil, mapProviderErrorToService(err)
	}
	status := strings.TrimSpace(queryResult.Status)
	if status == "" {
		status = constants.PaymentStatusPending
	}
	callbackInput := PaymentCallbackInput{
		PaymentID:   payment.ID,
		OrderNo:     payment.GatewayOrderNo,
		ChannelID:   channel.ID,
		Status:      status,
		ProviderRef: pickFirstNonEmpty(queryResult.ProviderRef, payment.ProviderRef),
		Amount:      queryResult.Amount,
		Currency:    strings.ToUpper(strings.TrimSpace(queryResult.Currency)),
		PaidAt:      queryResult.PaidAt,
		Payload:     queryResult.Payload,
	}
	updated, err := s.HandleCallback(callbackInput)
	if err != nil {
		if errors.Is(err, ErrPaymentStatusInvalid) {
			return nil, err
		}
		return nil, err
	}
	return updated, nil
}
