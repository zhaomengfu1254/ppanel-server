package common

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/hibiken/asynq"
	"github.com/perfect-panel/server/internal/config"
	"github.com/perfect-panel/server/pkg/constant"
	"github.com/perfect-panel/server/pkg/limit"
	"github.com/perfect-panel/server/pkg/random"
	"github.com/pkg/errors"
	"gorm.io/gorm"

	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/xerr"
	queue "github.com/perfect-panel/server/queue/types"
)

type SendEmailCodeLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

const (
	IntervalTime = 60
)

type VerifyTemplate struct {
	Type     uint8
	SiteLogo string
	SiteName string
	Expire   uint8
	Code     string
}

type CacheKeyPayload struct {
	Code   string `json:"code"`
	LastAt int64  `json:"lastAt"`
}

// EmailConfig 存储从数据库获取的邮箱配置
type EmailConfig struct {
	DomainSuffixList   string `json:"domain_suffix_list"`
	EnableDomainSuffix bool   `json:"enable_domain_suffix"`
	EnableNotify       bool   `json:"enable_notify"`
	EnableVerify       bool   `json:"enable_verify"`
	// 其他字段省略，因为我们只关注域名后缀白名单
}

// AuthMethod 表示数据库中的auth_method表
type AuthMethod struct {
	ID        int64     `gorm:"column:id;primaryKey"`
	Method    string    `gorm:"column:method"`
	Config    string    `gorm:"column:config"`
	Enabled   int       `gorm:"column:enabled"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// EmailAliasRule 定义邮箱别名规则
type EmailAliasRule struct {
	Domain          string         // 域名
	PlusAlias       bool           // 是否允许+号别名，如 user+alias@domain.com
	DotAlias        bool           // 是否允许点号别名，如 u.s.e.r@domain.com
	NormalizeRegexp *regexp.Regexp // 用于标准化邮箱别名的正则表达式
}

// 常见邮箱提供商的别名规则
var emailAliasRules = map[string]EmailAliasRule{
	"gmail.com": {
		Domain:          "gmail.com",
		PlusAlias:       true,
		DotAlias:        true,
		NormalizeRegexp: regexp.MustCompile(`\.|\+.*`), // 匹配点号和加号后的所有内容
	},
	"outlook.com": {
		Domain:          "outlook.com",
		PlusAlias:       true,
		DotAlias:        false,
		NormalizeRegexp: regexp.MustCompile(`\+.*`), // 只匹配加号后的所有内容
	},
	"hotmail.com": {
		Domain:          "hotmail.com",
		PlusAlias:       true,
		DotAlias:        false,
		NormalizeRegexp: regexp.MustCompile(`\+.*`),
	},
	"yahoo.com": {
		Domain:          "yahoo.com",
		PlusAlias:       true,
		DotAlias:        false,
		NormalizeRegexp: regexp.MustCompile(`\+.*`),
	},
}

// NewSendEmailCodeLogic Get verification code
func NewSendEmailCodeLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SendEmailCodeLogic {
	return &SendEmailCodeLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// 检查邮箱后缀是否在白名单中
func (l *SendEmailCodeLogic) isEmailSuffixAllowed(email string, allowedSuffixes []string) bool {
	emailParts := strings.Split(email, "@")
	if len(emailParts) != 2 {
		return false // 邮箱格式不正确
	}

	suffix := strings.ToLower(emailParts[1])

	for _, allowedSuffix := range allowedSuffixes {
		if strings.ToLower(strings.TrimSpace(allowedSuffix)) == suffix {
			return true
		}
	}

	return false
}

// 检查并标准化邮箱别名
// 返回值：标准化后的邮箱，是否为别名邮箱，错误信息
func (l *SendEmailCodeLogic) normalizeEmailAlias(email string) (string, bool, error) {
	emailParts := strings.Split(email, "@")
	if len(emailParts) != 2 {
		return "", false, errors.New("invalid email format")
	}

	username := emailParts[0]
	domain := strings.ToLower(emailParts[1])

	// 检查是否为已知的支持别名的域名
	rule, exists := emailAliasRules[domain]
	if !exists {
		// 对于未知域名，我们只检查是否包含+号，这是最常见的别名标记
		if strings.Contains(username, "+") {
			return "", true, nil
		}
		return email, false, nil
	}

	// 检查是否使用了别名
	isAlias := false

	// 检查+号别名
	if rule.PlusAlias && strings.Contains(username, "+") {
		isAlias = true
	}

	// 检查.号别名 (主要是Gmail)
	if rule.DotAlias && strings.Contains(username, ".") {
		isAlias = true
	}

	// 如果是别名，返回标准化的邮箱
	if isAlias {
		// 标准化用户名部分
		normalizedUsername := rule.NormalizeRegexp.ReplaceAllString(username, "")
		// 返回标准化后的邮箱
		normalizedEmail := normalizedUsername + "@" + domain
		return normalizedEmail, true, nil
	}

	return email, false, nil
}

// 从数据库获取邮箱配置
func (l *SendEmailCodeLogic) getEmailConfig() (*EmailConfig, error) {
	// 从auth_method表中获取method=email的配置
	var authMethod AuthMethod

	// 明确表名，并根据数据库结构查询
	err := l.svcCtx.DB.Table("auth_method").
		Where("method = ?", "email").
		First(&authMethod).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "Email auth method not found")
		}
		l.Errorw("[getEmailConfig]: Database error", logger.Field("error", err.Error()))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "Failed to get email config")
	}

	// 检查记录是否启用
	if authMethod.Enabled != 1 {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "Email auth method is disabled")
	}

	var config EmailConfig
	if err := json.Unmarshal([]byte(authMethod.Config), &config); err != nil {
		l.Errorw("[getEmailConfig]: JSON parse error",
			logger.Field("error", err.Error()),
			logger.Field("config", authMethod.Config))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "Failed to parse email config")
	}

	return &config, nil
}

func (l *SendEmailCodeLogic) SendEmailCode(req *types.SendCodeRequest) (resp *types.SendCodeResponse, err error) {
	// 获取邮箱配置
	emailConfig, err := l.getEmailConfig()
	if err != nil {
		l.Errorw("[SendEmailCode]: Failed to get email config", logger.Field("error", err.Error()))
		return nil, err
	}

	// 检查并标准化邮箱别名
	normalizedEmail, isAlias, err := l.normalizeEmailAlias(req.Email)
	if err != nil {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.InvalidParameters), "invalid email format")
	}

	// 如果是别名邮箱，拒绝请求
	if isAlias {
		l.Infow("[SendEmailCode]: Alias email detected",
			logger.Field("original", req.Email),
			logger.Field("normalized", normalizedEmail))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.InvalidEmailPrefix), "email aliases not allowed")
	}

	// 如果启用了域名后缀白名单
	if emailConfig.EnableDomainSuffix {
		// 解析域名后缀列表
		suffixList := strings.Split(emailConfig.DomainSuffixList, "\n")
		// 检查邮箱后缀是否在白名单中
		if !l.isEmailSuffixAllowed(req.Email, suffixList) {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.EmailNotInWhite), "email domain not allowed")
		}
	}

	// Check if there is Redis in the code
	cacheKey := fmt.Sprintf("%s:%s:%s", config.AuthCodeCacheKey, constant.ParseVerifyType(req.Type), req.Email)
	// Check if the limit is exceeded of current request
	limiter := limit.NewPeriodLimit(60, 1, l.svcCtx.Redis, fmt.Sprintf("%s:%s:%s", config.SendIntervalKeyPrefix, "email", constant.ParseVerifyType(req.Type)))
	permit, err := limiter.Take(req.Email)
	if err != nil {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "Failed to take limit")
	}
	if !limiter.ParsePermitState(permit) {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.TooManyRequests), "send email too many requests")
	}
	// Check if the limit is exceeded of today
	permit, err = l.svcCtx.AuthLimiter.Take(fmt.Sprintf("%s:%s:%s", "email", constant.ParseVerifyType(req.Type), req.Email))
	if err != nil {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "Failed to take limit")
	}
	if !l.svcCtx.AuthLimiter.ParsePermitState(permit) {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.TodaySendCountExceedsLimit), "send email too many requests")
	}
	m, err := l.svcCtx.UserModel.FindUserAuthMethodByOpenID(l.ctx, "email", req.Email)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "FindUserAuthMethodByOpenID error")
	}
	if constant.ParseVerifyType(req.Type) == constant.Register && m.Id > 0 {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserExist), "email already bind")
	} else if constant.ParseVerifyType(req.Type) == constant.Security && m.Id == 0 {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserNotExist), "email not bind")
	}

	var payload CacheKeyPayload
	var taskPayload queue.SendEmailPayload
	// Generate verification code
	code := random.Key(6, 0)
	taskPayload.Email = req.Email
	taskPayload.Subject = "Verification code"
	content, err := l.initTemplate(req.Type, code)
	if err != nil {
		l.Logger.Error("[SendEmailCode]: InitTemplate Error", logger.Field("error", err.Error()))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "Failed to init template")
	}
	taskPayload.Content = content
	// Save to Redis
	payload = CacheKeyPayload{
		Code:   code,
		LastAt: time.Now().Unix(),
	}
	// Marshal the payload
	val, _ := json.Marshal(payload)
	if err = l.svcCtx.Redis.Set(l.ctx, cacheKey, string(val), time.Second*IntervalTime*5).Err(); err != nil {
		l.Errorw("[SendEmailCode]: Redis Error", logger.Field("error", err.Error()), logger.Field("cacheKey", cacheKey))
		return nil, errors.Wrap(xerr.NewErrCode(xerr.ERROR), "Failed to set verification code")
	}

	// Marshal the task payload
	payloadBuy, err := json.Marshal(taskPayload)
	if err != nil {
		l.Errorw("[SendEmailCode]: Marshal Error", logger.Field("error", err.Error()))
		return nil, errors.Wrap(xerr.NewErrCode(xerr.ERROR), "Failed to marshal task payload")
	}
	// Create a queue task
	task := asynq.NewTask(queue.ForthwithSendEmail, payloadBuy, asynq.MaxRetry(3))
	// Enqueue the task
	taskInfo, err := l.svcCtx.Queue.Enqueue(task)
	if err != nil {
		l.Errorw("[SendEmailCode]: Enqueue Error", logger.Field("error", err.Error()), logger.Field("payload", string(payloadBuy)))
		return nil, errors.Wrap(xerr.NewErrCode(xerr.ERROR), "Failed to enqueue task")
	}
	l.Infow("[SendEmailCode]: Enqueue Success", logger.Field("taskID", taskInfo.ID), logger.Field("payload", string(payloadBuy)))
	if l.svcCtx.Config.Model == constant.DevMode {
		return &types.SendCodeResponse{
			Code:   payload.Code,
			Status: true,
		}, nil
	} else {
		return &types.SendCodeResponse{
			Status: true,
		}, nil
	}
}

func (l *SendEmailCodeLogic) initTemplate(t uint8, code string) (string, error) {
	data := VerifyTemplate{
		Type:     t,
		SiteLogo: l.svcCtx.Config.Site.SiteLogo,
		SiteName: l.svcCtx.Config.Site.SiteName,
		Expire:   5,
		Code:     code,
	}
	tpl, err := template.New("verify").Parse(l.svcCtx.Config.Email.VerifyEmailTemplate)
	if err != nil {
		return "", err
	}
	var result bytes.Buffer
	err = tpl.Execute(&result, data)
	if err != nil {
		return "", err
	}
	return result.String(), nil
}
