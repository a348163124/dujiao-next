package service

import (
	"testing"
	"time"

	"github.com/dujiao-next/internal/crypto"
)

func TestPrepareTOTPEnableRejectsExpiredPendingBeforeSideEffects(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	expiredAt := base.Add(-time.Second)
	key := crypto.DeriveKey("totp-enable-test")

	checkFailuresCalled := false
	bumpFailureCalled := false
	verifyCodeCalled := false

	result, err := prepareTOTPEnable(totpEnableInput{
		accountID:         42,
		encKey:            key,
		pendingSecret:     "not-yet-decrypted",
		pendingExpiresAt:  &expiredAt,
		code:              "123456",
		recoveryCodeCount: 2,
		now: func() time.Time {
			return base
		},
		checkFailures: func(accountID uint) error {
			if accountID != 42 {
				t.Fatalf("expected account ID 42, got %d", accountID)
			}
			checkFailuresCalled = true
			return nil
		},
		verifyCode: func(secret, code string) bool {
			verifyCodeCalled = true
			return true
		},
		bumpFailure: func(accountID uint) {
			if accountID != 42 {
				t.Fatalf("expected account ID 42, got %d", accountID)
			}
			bumpFailureCalled = true
		},
	})
	if err != ErrTOTPPendingExpired {
		t.Fatalf("expected ErrTOTPPendingExpired, got %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result for expired pending secret")
	}
	if checkFailuresCalled || verifyCodeCalled || bumpFailureCalled {
		t.Fatalf("expected no side effects before pending expiry is accepted, got check=%v verify=%v bump=%v",
			checkFailuresCalled, verifyCodeCalled, bumpFailureCalled)
	}
}

func TestPrepareTOTPEnableBumpsFailureWhenCodeInvalid(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	expiresAt := base.Add(time.Minute)
	key := crypto.DeriveKey("totp-enable-test")
	encryptedSecret, err := crypto.Encrypt(key, "shared-secret")
	if err != nil {
		t.Fatalf("encrypt pending secret: %v", err)
	}

	checkFailuresCalls := 0
	bumpFailureCalls := 0
	verifySecret := ""
	verifyCode := ""

	result, err := prepareTOTPEnable(totpEnableInput{
		accountID:         43,
		encKey:            key,
		pendingSecret:     encryptedSecret,
		pendingExpiresAt:  &expiresAt,
		code:              "000000",
		recoveryCodeCount: 2,
		now: func() time.Time {
			return base
		},
		checkFailures: func(accountID uint) error {
			if accountID != 43 {
				t.Fatalf("expected account ID 43, got %d", accountID)
			}
			checkFailuresCalls++
			return nil
		},
		verifyCode: func(secret, code string) bool {
			verifySecret = secret
			verifyCode = code
			return false
		},
		bumpFailure: func(accountID uint) {
			if accountID != 43 {
				t.Fatalf("expected account ID 43, got %d", accountID)
			}
			bumpFailureCalls++
		},
	})
	if err != ErrTOTPCodeInvalid {
		t.Fatalf("expected ErrTOTPCodeInvalid, got %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result for invalid code")
	}
	if checkFailuresCalls != 1 {
		t.Fatalf("expected checkFailures once, got %d", checkFailuresCalls)
	}
	if bumpFailureCalls != 1 {
		t.Fatalf("expected bumpFailure once, got %d", bumpFailureCalls)
	}
	if verifySecret != "shared-secret" || verifyCode != "000000" {
		t.Fatalf("expected verifier to receive decrypted secret and submitted code, got secret=%q code=%q", verifySecret, verifyCode)
	}
}

func TestPrepareTOTPEnableReturnsEncryptedSecretRecoveryCodesAndEnabledAt(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	enabledAt := base.Add(2 * time.Second)
	expiresAt := base.Add(time.Minute)
	key := crypto.DeriveKey("totp-enable-test")
	encryptedSecret, err := crypto.Encrypt(key, "shared-secret")
	if err != nil {
		t.Fatalf("encrypt pending secret: %v", err)
	}

	nowCalls := 0
	checkFailuresCalls := 0
	bumpFailureCalls := 0

	result, err := prepareTOTPEnable(totpEnableInput{
		accountID:         44,
		encKey:            key,
		pendingSecret:     encryptedSecret,
		pendingExpiresAt:  &expiresAt,
		code:              "123456",
		recoveryCodeCount: 3,
		now: func() time.Time {
			nowCalls++
			if nowCalls == 1 {
				return base
			}
			return enabledAt
		},
		checkFailures: func(accountID uint) error {
			if accountID != 44 {
				t.Fatalf("expected account ID 44, got %d", accountID)
			}
			checkFailuresCalls++
			return nil
		},
		verifyCode: func(secret, code string) bool {
			return secret == "shared-secret" && code == "123456"
		},
		bumpFailure: func(accountID uint) {
			if accountID != 44 {
				t.Fatalf("expected account ID 44, got %d", accountID)
			}
			bumpFailureCalls++
		},
	})
	if err != nil {
		t.Fatalf("prepare enable: %v", err)
	}
	if result == nil {
		t.Fatalf("expected result")
	}
	if checkFailuresCalls != 1 {
		t.Fatalf("expected checkFailures once, got %d", checkFailuresCalls)
	}
	if bumpFailureCalls != 0 {
		t.Fatalf("expected no failure bump, got %d", bumpFailureCalls)
	}
	if result.enabledAt != enabledAt {
		t.Fatalf("expected enabledAt %v, got %v", enabledAt, result.enabledAt)
	}
	decryptedSecret, err := crypto.Decrypt(key, result.encryptedSecret)
	if err != nil {
		t.Fatalf("decrypt enabled secret: %v", err)
	}
	if decryptedSecret != "shared-secret" {
		t.Fatalf("expected re-encrypted shared secret, got %q", decryptedSecret)
	}
	if len(result.recoveryCodes) != 3 {
		t.Fatalf("expected 3 plaintext recovery codes, got %d", len(result.recoveryCodes))
	}
	entries, err := decodeRecoveryCodesJSON(result.recoveryCodesJSON)
	if err != nil {
		t.Fatalf("decode recovery codes: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 recovery code hashes, got %d", len(entries))
	}
}
