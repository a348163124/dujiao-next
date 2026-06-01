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

// TOTP 相关错误
var (
	ErrTOTPAlreadyEnabled  = errors.New("totp already enabled")
	ErrTOTPNotEnabled      = errors.New("totp not enabled")
	ErrTOTPPendingExpired  = errors.New("totp pending secret expired")
	ErrTOTPCodeInvalid     = errors.New("totp code invalid")
	ErrTOTPRecoveryInvalid = errors.New("recovery code invalid or used")
	ErrTOTPTooManyAttempts = errors.New("too many failed attempts")
	ErrTOTPCannotResetSelf = errors.New("cannot reset self via super admin endpoint")
)

const (
	totpIssuerDefault     = "Dujiao-Next"
	totpPendingTTL        = 10 * time.Minute
	totpEnableMaxFailures = 5
	totpRecoveryCodeCount = 10
	totpDigits            = 6
	totpPeriod            = 30
	totpSkew              = 1
)

// TOTPService TOTP 业务服务
//
// 注：审计日志（admin_login_log）由 handler 层在调用前后写入，service 不直接持有 logRepo。
type TOTPService struct {
	cfg       *config.Config
	encKey    []byte
	adminRepo repository.AdminRepository
	redis     *redis.Client
	now       func() time.Time
}

// NewTOTPService 创建实例
func NewTOTPService(cfg *config.Config, adminRepo repository.AdminRepository, rds *redis.Client) *TOTPService {
	return &TOTPService{
		cfg:       cfg,
		encKey:    crypto.DeriveKey(cfg.App.SecretKey),
		adminRepo: adminRepo,
		redis:     rds,
		now:       time.Now,
	}
}

// Status 当前账号 2FA 状态
type Status struct {
	Enabled                bool       `json:"enabled"`
	EnabledAt              *time.Time `json:"enabled_at,omitempty"`
	RecoveryCodesRemaining int        `json:"recovery_codes_remaining"`
	RecoveryCodesTotal     int        `json:"recovery_codes_total"`
}

// GetStatus 查询状态
func (s *TOTPService) GetStatus(adminID uint) (*Status, error) {
	admin, err := s.adminRepo.GetByID(adminID)
	if err != nil {
		return nil, err
	}
	if admin == nil {
		return nil, ErrNotFound
	}
	st := &Status{Enabled: admin.TOTPEnabledAt != nil, EnabledAt: admin.TOTPEnabledAt}
	if admin.RecoveryCodes != "" {
		entries, err := decodeRecoveryCodesJSON(admin.RecoveryCodes)
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

// SetupResult /2fa/setup 响应
type SetupResult struct {
	Secret     string    `json:"secret"`
	OtpauthURL string    `json:"otpauth_url"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Setup 生成 pending secret + otpauth URL
func (s *TOTPService) Setup(adminID uint) (*SetupResult, error) {
	admin, err := s.adminRepo.GetByID(adminID)
	if err != nil {
		return nil, err
	}
	if admin == nil {
		return nil, ErrNotFound
	}
	if admin.TOTPEnabledAt != nil {
		return nil, ErrTOTPAlreadyEnabled
	}
	issuer := totpIssuerDefault
	if s.cfg != nil && strings.TrimSpace(s.cfg.App.TOTPIssuer) != "" {
		issuer = strings.TrimSpace(s.cfg.App.TOTPIssuer)
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: admin.Username,
		Period:      totpPeriod,
		Digits:      totpDigits,
	})
	if err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	encSecret, err := crypto.Encrypt(s.encKey, key.Secret())
	if err != nil {
		return nil, fmt.Errorf("encrypt secret: %w", err)
	}
	expiresAt := s.now().Add(totpPendingTTL)
	if err := s.adminRepo.UpdateTOTPPending(adminID, encSecret, expiresAt); err != nil {
		return nil, err
	}
	if s.redis != nil {
		_ = s.redis.Del(context.Background(), enableFailKey(adminID)).Err()
	}
	return &SetupResult{
		Secret:     key.Secret(),
		OtpauthURL: key.URL(),
		ExpiresAt:  expiresAt,
	}, nil
}

// EnableResult /2fa/enable 响应
type EnableResult struct {
	EnabledAt     time.Time `json:"enabled_at"`
	RecoveryCodes []string  `json:"recovery_codes"`
}

// Enable 校验首次 code，启用 2FA，生成恢复码
func (s *TOTPService) Enable(adminID uint, code string) (*EnableResult, error) {
	prepared, err := enableTOTPFor(s, totpEnableInput{
		accountID:         adminID,
		encKey:            s.encKey,
		code:              code,
		recoveryCodeCount: totpRecoveryCodeCount,
		now:               s.now,
	})
	if err != nil {
		return nil, err
	}
	return &EnableResult{EnabledAt: prepared.enabledAt, RecoveryCodes: prepared.recoveryCodes}, nil
}

// Disable 关闭 2FA：使用 TOTP code 或恢复码二次确认
func (s *TOTPService) Disable(adminID uint, code string, isRecoveryCode bool) error {
	admin, err := s.adminRepo.GetByID(adminID)
	if err != nil {
		return err
	}
	if admin == nil {
		return ErrNotFound
	}
	if admin.TOTPEnabledAt == nil {
		return ErrTOTPNotEnabled
	}
	if isRecoveryCode {
		if err := s.consumeRecoveryCode(admin, code); err != nil {
			return err
		}
	} else {
		secret, err := crypto.Decrypt(s.encKey, admin.TOTPSecret)
		if err != nil {
			return fmt.Errorf("decrypt secret: %w", err)
		}
		if !s.verifyCode(secret, code) {
			return ErrTOTPCodeInvalid
		}
	}
	if err := s.adminRepo.ClearTOTP(adminID); err != nil {
		return err
	}
	// 立即清除 Redis 鉴权快照，防止旧 TokenVersion 在 cache TTL 内继续放行旧 token
	_ = cache.DelAdminAuthState(context.Background(), adminID)
	return nil
}

// RegenerateRecoveryCodes 重新生成恢复码（必须当前 TOTP code，不允许用恢复码）
func (s *TOTPService) RegenerateRecoveryCodes(adminID uint, code string) ([]string, error) {
	admin, err := s.adminRepo.GetByID(adminID)
	if err != nil {
		return nil, err
	}
	if admin == nil {
		return nil, ErrNotFound
	}
	if admin.TOTPEnabledAt == nil {
		return nil, ErrTOTPNotEnabled
	}
	secret, err := crypto.Decrypt(s.encKey, admin.TOTPSecret)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret: %w", err)
	}
	if !s.verifyCode(secret, code) {
		return nil, ErrTOTPCodeInvalid
	}
	plaintext, codesJSON, err := s.generateRecoveryCodes(totpRecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	if err := s.adminRepo.UpdateRecoveryCodes(adminID, codesJSON); err != nil {
		return nil, err
	}
	return plaintext, nil
}

// VerifyChallengeCode 登录第二步：验证 TOTP code（不消耗恢复码）
func (s *TOTPService) VerifyChallengeCode(adminID uint, code string) error {
	admin, err := s.adminRepo.GetByID(adminID)
	if err != nil {
		return err
	}
	if admin == nil {
		return ErrNotFound
	}
	if admin.TOTPEnabledAt == nil {
		return ErrTOTPNotEnabled
	}
	secret, err := crypto.Decrypt(s.encKey, admin.TOTPSecret)
	if err != nil {
		return fmt.Errorf("decrypt secret: %w", err)
	}
	if !s.verifyCode(secret, code) {
		return ErrTOTPCodeInvalid
	}
	return nil
}

// VerifyChallengeRecoveryCode 登录第二步：用恢复码（消耗一个）
func (s *TOTPService) VerifyChallengeRecoveryCode(adminID uint, code string) error {
	admin, err := s.adminRepo.GetByID(adminID)
	if err != nil {
		return err
	}
	if admin == nil {
		return ErrNotFound
	}
	if admin.TOTPEnabledAt == nil {
		return ErrTOTPNotEnabled
	}
	return s.consumeRecoveryCode(admin, code)
}

// AdminReset 超管强制清空目标管理员 2FA
func (s *TOTPService) AdminReset(operatorID, targetID uint) error {
	if operatorID == targetID {
		return ErrTOTPCannotResetSelf
	}
	target, err := s.adminRepo.GetByID(targetID)
	if err != nil {
		return err
	}
	if target == nil {
		return ErrNotFound
	}
	if err := s.adminRepo.ClearTOTP(targetID); err != nil {
		return err
	}
	// 直接删除缓存，强制下次请求从 DB 加载新的 TokenVersion 并失效旧 token
	_ = cache.DelAdminAuthState(context.Background(), targetID)
	_ = target // target 仅用于上面的 nil 检查
	return nil
}

// ---- 内部辅助 ----

func (s *TOTPService) verifyCode(secret, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	valid, _ := totp.ValidateCustom(code, secret, s.now(), totp.ValidateOpts{
		Period: totpPeriod,
		Skew:   totpSkew,
		Digits: totpDigits,
	})
	return valid
}

func (s *TOTPService) generateRecoveryCodes(n int) (plaintext []string, codesJSON string, err error) {
	return generateRecoveryCodesPair(n)
}

func (s *TOTPService) consumeRecoveryCode(admin *models.Admin, code string) error {
	js, err := matchAndConsumeRecoveryCode(admin.RecoveryCodes, code, s.now())
	if err != nil {
		return err
	}
	return s.adminRepo.UpdateRecoveryCodes(admin.ID, js)
}

func (s *TOTPService) loadTOTPEnableSubject(adminID uint) (totpEnableSubject, error) {
	admin, err := s.adminRepo.GetByID(adminID)
	if err != nil || admin == nil {
		return totpEnableSubject{}, err
	}
	return totpEnableSubject{
		exists:           true,
		enabledAt:        admin.TOTPEnabledAt,
		pendingSecret:    admin.TOTPPendingSecret,
		pendingExpiresAt: admin.TOTPPendingExpiresAt,
	}, nil
}

func (s *TOTPService) updateTOTPEnabledFromPrepared(adminID uint, result *totpEnableResult) error {
	return s.adminRepo.UpdateTOTPEnabled(adminID, result.encryptedSecret, result.enabledAt, result.recoveryCodesJSON)
}

func (s *TOTPService) clearTOTPEnableFailures(adminID uint) {
	if s.redis != nil {
		_ = s.redis.Del(context.Background(), enableFailKey(adminID)).Err()
	}
}

func enableFailKey(adminID uint) string {
	return fmt.Sprintf("2fa:enable:%d:fails", adminID)
}

func (s *TOTPService) checkEnableFailures(adminID uint) error {
	if s.redis == nil {
		return nil
	}
	v, err := s.redis.Get(context.Background(), enableFailKey(adminID)).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil
	}
	if v >= totpEnableMaxFailures {
		_ = s.adminRepo.UpdateTOTPPending(adminID, "", time.Time{})
		_ = s.redis.Del(context.Background(), enableFailKey(adminID)).Err()
		return ErrTOTPTooManyAttempts
	}
	return nil
}

func (s *TOTPService) bumpEnableFailures(adminID uint) {
	if s.redis == nil {
		return
	}
	ctx := context.Background()
	cnt, err := s.redis.Incr(ctx, enableFailKey(adminID)).Result()
	if err == nil && cnt == 1 {
		_ = s.redis.Expire(ctx, enableFailKey(adminID), totpPendingTTL).Err()
	}
}

// 辅助：在 ChallengeToken jti 维度记录失败 / 检查 / revoke
func ChallengeFailKey(jti string) string    { return "2fa:challenge:" + jti + ":fails" }
func ChallengeRevokedKey(jti string) string { return "2fa:challenge:" + jti + ":revoked" }

// ChallengeMaxFailures 公开：handler 也用
const ChallengeMaxFailures = 5

// ChallengeTTL 挑战 token TTL
const ChallengeTTL = 5 * time.Minute
