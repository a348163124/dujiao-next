package service

import (
	"fmt"
	"time"

	"github.com/dujiao-next/internal/crypto"
)

type totpEnableInput struct {
	accountID         uint
	encKey            []byte
	pendingSecret     string
	pendingExpiresAt  *time.Time
	code              string
	recoveryCodeCount int
	now               func() time.Time
	checkFailures     func(uint) error
	verifyCode        func(secret, code string) bool
	bumpFailure       func(uint)
}

type totpEnableResult struct {
	encryptedSecret   string
	recoveryCodes     []string
	recoveryCodesJSON string
	enabledAt         time.Time
}

type totpEnableSubject struct {
	exists           bool
	enabledAt        *time.Time
	pendingSecret    string
	pendingExpiresAt *time.Time
}

type totpEnableStore interface {
	loadTOTPEnableSubject(accountID uint) (totpEnableSubject, error)
	updateTOTPEnabledFromPrepared(accountID uint, result *totpEnableResult) error
	clearTOTPEnableFailures(accountID uint)
	checkEnableFailures(accountID uint) error
	verifyCode(secret, code string) bool
	bumpEnableFailures(accountID uint)
}

func enableTOTPFor(store totpEnableStore, input totpEnableInput) (*totpEnableResult, error) {
	subject, err := store.loadTOTPEnableSubject(input.accountID)
	if err != nil {
		return nil, err
	}
	if !subject.exists {
		return nil, ErrNotFound
	}
	if subject.enabledAt != nil {
		return nil, ErrTOTPAlreadyEnabled
	}

	input.pendingSecret = subject.pendingSecret
	input.pendingExpiresAt = subject.pendingExpiresAt
	input.checkFailures = store.checkEnableFailures
	input.verifyCode = store.verifyCode
	input.bumpFailure = store.bumpEnableFailures

	result, err := prepareTOTPEnable(input)
	if err != nil {
		return nil, err
	}
	if err := store.updateTOTPEnabledFromPrepared(input.accountID, result); err != nil {
		return nil, err
	}
	store.clearTOTPEnableFailures(input.accountID)
	return result, nil
}

func prepareTOTPEnable(input totpEnableInput) (*totpEnableResult, error) {
	if input.pendingSecret == "" || input.pendingExpiresAt == nil || input.now().After(*input.pendingExpiresAt) {
		return nil, ErrTOTPPendingExpired
	}
	if input.checkFailures != nil {
		if err := input.checkFailures(input.accountID); err != nil {
			return nil, err
		}
	}
	secret, err := crypto.Decrypt(input.encKey, input.pendingSecret)
	if err != nil {
		return nil, fmt.Errorf("decrypt pending: %w", err)
	}
	if input.verifyCode == nil || !input.verifyCode(secret, input.code) {
		if input.bumpFailure != nil {
			input.bumpFailure(input.accountID)
		}
		return nil, ErrTOTPCodeInvalid
	}
	encryptedSecret, err := crypto.Encrypt(input.encKey, secret)
	if err != nil {
		return nil, fmt.Errorf("re-encrypt secret: %w", err)
	}
	recoveryCodes, recoveryCodesJSON, err := generateRecoveryCodesPair(input.recoveryCodeCount)
	if err != nil {
		return nil, err
	}
	return &totpEnableResult{
		encryptedSecret:   encryptedSecret,
		recoveryCodes:     recoveryCodes,
		recoveryCodesJSON: recoveryCodesJSON,
		enabledAt:         input.now(),
	}, nil
}
