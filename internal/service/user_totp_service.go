package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/crypto"
	"github.com/dujiao-next/internal/models"
	"github.com/dujiao-next/internal/repository"

	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
)

const (
	userTotpIssuerDefault     = "Dujiao-Next-User"
	userTotpPendingTTL        = 10 * time.Minute
	userTotpEnableMaxFailures = 5
	userTotpRecoveryCodeCount = 10
	userTotpDigits            = 6
	userTotpPeriod            = 30
	userTotpSkew              = 1
)

// UserChallengePurpose2FA 用户 2FA 挑战 token purpose 常量
const UserChallengePurpose2FA = "user_2fa_challenge"

// UserChallengeTTL 用户挑战 token 有效期
const UserChallengeTTL = 5 * time.Minute

// UserChallengeMaxFailures 用户挑战最大失败次数
const UserChallengeMaxFailures = 5

// UserChallengeFailKey 失败计数 redis key
func UserChallengeFailKey(jti string) string { return "2fa:user:challenge:" + jti + ":fails" }

// UserChallengeRevokedKey 撤销标记 redis key
func UserChallengeRevokedKey(jti string) string { return "2fa:user:challenge:" + jti + ":revoked" }

// UserTOTPService 用户 TOTP 业务服务
type UserTOTPService struct {
	cfg      *config.Config
	encKey   []byte
	userRepo repository.UserRepository
	redis    *redis.Client
	now      func() time.Time
}

// NewUserTOTPService 创建实例
func NewUserTOTPService(cfg *config.Config, userRepo repository.UserRepository, rds *redis.Client) *UserTOTPService {
	return &UserTOTPService{
		cfg:      cfg,
		encKey:   crypto.DeriveKey(cfg.App.SecretKey),
		userRepo: userRepo,
		redis:    rds,
		now:      time.Now,
	}
}

// UserTOTPStatus 用户 2FA 状态
type UserTOTPStatus struct {
	Enabled                bool       `json:"enabled"`
	EnabledAt              *time.Time `json:"enabled_at,omitempty"`
	RecoveryCodesRemaining int        `json:"recovery_codes_remaining"`
	RecoveryCodesTotal     int        `json:"recovery_codes_total"`
}

// UserTOTPSetupResult /me/2fa/setup 响应
type UserTOTPSetupResult struct {
	Secret     string    `json:"secret"`
	OtpauthURL string    `json:"otpauth_url"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// UserTOTPEnableResult /me/2fa/enable 响应
type UserTOTPEnableResult struct {
	EnabledAt     time.Time `json:"enabled_at"`
	RecoveryCodes []string  `json:"recovery_codes"`
	// 启用 2FA 时同步 bump TokenVersion 强制其他设备下线，因此当前 session 也需要换发新的 JWT
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GetStatus 查询状态
func (s *UserTOTPService) GetStatus(userID uint) (*UserTOTPStatus, error) {
	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrNotFound
	}
	st := &UserTOTPStatus{Enabled: user.TOTPEnabledAt != nil, EnabledAt: user.TOTPEnabledAt}
	if user.RecoveryCodes != "" {
		entries, err := decodeRecoveryCodesJSON(user.RecoveryCodes)
		if err == nil {
			st.RecoveryCodesTotal = len(entries)
			for _, e := range entries {
				if e.UsedAt == nil {
					st.RecoveryCodesRemaining++
				}
			}
		}
	}
	return st, nil
}

// Setup 生成 pending secret + otpauth URL
func (s *UserTOTPService) Setup(userID uint) (*UserTOTPSetupResult, error) {
	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrNotFound
	}
	if user.TOTPEnabledAt != nil {
		return nil, ErrTOTPAlreadyEnabled
	}
	issuer := userTotpIssuerDefault
	if s.cfg != nil && strings.TrimSpace(s.cfg.App.TOTPIssuer) != "" {
		issuer = strings.TrimSpace(s.cfg.App.TOTPIssuer)
	}
	accountName := user.Email
	if strings.TrimSpace(accountName) == "" {
		accountName = fmt.Sprintf("user-%d", user.ID)
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: accountName,
		Period:      userTotpPeriod,
		Digits:      userTotpDigits,
	})
	if err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	encSecret, err := crypto.Encrypt(s.encKey, key.Secret())
	if err != nil {
		return nil, fmt.Errorf("encrypt secret: %w", err)
	}
	expiresAt := s.now().Add(userTotpPendingTTL)
	if err := s.userRepo.UpdateTOTPPending(userID, encSecret, expiresAt); err != nil {
		return nil, err
	}
	if s.redis != nil {
		_ = s.redis.Del(context.Background(), userEnableFailKey(userID)).Err()
	}
	return &UserTOTPSetupResult{
		Secret:     key.Secret(),
		OtpauthURL: key.URL(),
		ExpiresAt:  expiresAt,
	}, nil
}

// Enable 校验首次 code，启用 2FA，生成恢复码
func (s *UserTOTPService) Enable(userID uint, code string) (*UserTOTPEnableResult, error) {
	prepared, err := enableTOTPFor(s, totpEnableInput{
		accountID:         userID,
		encKey:            s.encKey,
		code:              code,
		recoveryCodeCount: userTotpRecoveryCodeCount,
		now:               s.now,
	})
	if err != nil {
		return nil, err
	}
	return &UserTOTPEnableResult{EnabledAt: prepared.enabledAt, RecoveryCodes: prepared.recoveryCodes}, nil
}

// Disable 关闭 2FA
func (s *UserTOTPService) Disable(userID uint, code string, isRecoveryCode bool) error {
	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		return err
	}
	if user == nil {
		return ErrNotFound
	}
	if user.TOTPEnabledAt == nil {
		return ErrTOTPNotEnabled
	}
	if isRecoveryCode {
		if err := s.consumeRecoveryCode(user, code); err != nil {
			return err
		}
	} else {
		secret, err := crypto.Decrypt(s.encKey, user.TOTPSecret)
		if err != nil {
			return fmt.Errorf("decrypt secret: %w", err)
		}
		if !s.verifyCode(secret, code) {
			return ErrTOTPCodeInvalid
		}
	}
	if err := s.userRepo.ClearTOTP(userID); err != nil {
		return err
	}
	_ = cache.DelUserAuthState(context.Background(), userID)
	return nil
}

// RegenerateRecoveryCodes 重新生成恢复码（必须当前 TOTP code）
func (s *UserTOTPService) RegenerateRecoveryCodes(userID uint, code string) ([]string, error) {
	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrNotFound
	}
	if user.TOTPEnabledAt == nil {
		return nil, ErrTOTPNotEnabled
	}
	secret, err := crypto.Decrypt(s.encKey, user.TOTPSecret)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret: %w", err)
	}
	if !s.verifyCode(secret, code) {
		return nil, ErrTOTPCodeInvalid
	}
	plaintext, codesJSON, err := s.generateRecoveryCodes(userTotpRecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	if err := s.userRepo.UpdateRecoveryCodes(userID, codesJSON); err != nil {
		return nil, err
	}
	return plaintext, nil
}

// VerifyChallengeCode 登录第二步：验证 TOTP code
func (s *UserTOTPService) VerifyChallengeCode(userID uint, code string) error {
	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		return err
	}
	if user == nil {
		return ErrNotFound
	}
	if user.TOTPEnabledAt == nil {
		return ErrTOTPNotEnabled
	}
	secret, err := crypto.Decrypt(s.encKey, user.TOTPSecret)
	if err != nil {
		return fmt.Errorf("decrypt secret: %w", err)
	}
	if !s.verifyCode(secret, code) {
		return ErrTOTPCodeInvalid
	}
	return nil
}

// VerifyChallengeRecoveryCode 登录第二步：用恢复码（消耗一个）
func (s *UserTOTPService) VerifyChallengeRecoveryCode(userID uint, code string) error {
	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		return err
	}
	if user == nil {
		return ErrNotFound
	}
	if user.TOTPEnabledAt == nil {
		return ErrTOTPNotEnabled
	}
	return s.consumeRecoveryCode(user, code)
}

// AdminResetUser2FA 管理员强制清空目标用户 2FA。
// 使用场景：用户同时丢失 TOTP 设备与所有恢复码，向管理员申诉后由管理员协助解绑。
// 与用户自助 Disable 不同：不需要 code/recovery code，直接清空。
// 同步 bump TokenVersion 强制其他设备下线（由 ClearTOTP 完成）。
// operatorID 仅用于让调用方留痕；service 内部不依赖它，但要求非零以避免来路不明的调用绕过审计。
// 返回 (targetUser, error)：targetUser 供 handler 写审计日志（邮箱等）。
func (s *UserTOTPService) AdminResetUser2FA(operatorID, targetID uint) (*models.User, error) {
	if operatorID == 0 {
		return nil, fmt.Errorf("operatorID is required for audit")
	}
	user, err := s.userRepo.GetByID(targetID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrNotFound
	}
	if user.TOTPEnabledAt == nil {
		return nil, ErrTOTPNotEnabled
	}
	if err := s.userRepo.ClearTOTP(targetID); err != nil {
		return nil, err
	}
	_ = cache.DelUserAuthState(context.Background(), targetID)
	return user, nil
}

// ---- 内部辅助 ----

func (s *UserTOTPService) verifyCode(secret, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	valid, _ := totp.ValidateCustom(code, secret, s.now(), totp.ValidateOpts{
		Period: userTotpPeriod,
		Skew:   userTotpSkew,
		Digits: userTotpDigits,
	})
	return valid
}

func (s *UserTOTPService) generateRecoveryCodes(n int) (plaintext []string, codesJSON string, err error) {
	return generateRecoveryCodesPair(n)
}

func (s *UserTOTPService) consumeRecoveryCode(user *models.User, code string) error {
	js, err := matchAndConsumeRecoveryCode(user.RecoveryCodes, code, s.now())
	if err != nil {
		return err
	}
	return s.userRepo.UpdateRecoveryCodes(user.ID, js)
}

func (s *UserTOTPService) loadTOTPEnableSubject(userID uint) (totpEnableSubject, error) {
	user, err := s.userRepo.GetByID(userID)
	if err != nil || user == nil {
		return totpEnableSubject{}, err
	}
	return totpEnableSubject{
		exists:           true,
		enabledAt:        user.TOTPEnabledAt,
		pendingSecret:    user.TOTPPendingSecret,
		pendingExpiresAt: user.TOTPPendingExpiresAt,
	}, nil
}

func (s *UserTOTPService) updateTOTPEnabledFromPrepared(userID uint, result *totpEnableResult) error {
	return s.userRepo.UpdateTOTPEnabled(userID, result.encryptedSecret, result.enabledAt, result.recoveryCodesJSON)
}

func (s *UserTOTPService) clearTOTPEnableFailures(userID uint) {
	if s.redis != nil {
		_ = s.redis.Del(context.Background(), userEnableFailKey(userID)).Err()
	}
}

func userEnableFailKey(userID uint) string {
	return fmt.Sprintf("2fa:user:enable:%d:fails", userID)
}

func (s *UserTOTPService) checkEnableFailures(userID uint) error {
	if s.redis == nil {
		return nil
	}
	v, err := s.redis.Get(context.Background(), userEnableFailKey(userID)).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil
	}
	if v >= userTotpEnableMaxFailures {
		_ = s.userRepo.UpdateTOTPPending(userID, "", time.Time{})
		_ = s.redis.Del(context.Background(), userEnableFailKey(userID)).Err()
		return ErrTOTPTooManyAttempts
	}
	return nil
}

func (s *UserTOTPService) bumpEnableFailures(userID uint) {
	if s.redis == nil {
		return
	}
	ctx := context.Background()
	cnt, err := s.redis.Incr(ctx, userEnableFailKey(userID)).Result()
	if err == nil && cnt == 1 {
		_ = s.redis.Expire(ctx, userEnableFailKey(userID), userTotpPendingTTL).Err()
	}
}
