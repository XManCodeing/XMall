package application

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/crypto"
	"github.com/dujiao-next/internal/logger"
	siteconnectioncontract "github.com/dujiao-next/internal/modules/siteconnection/contract"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/shared/httpintegration"
	"github.com/dujiao-next/internal/upstream"

	"github.com/shopspring/decimal"
)

// MarkupReapplier 在连接定价配置（汇率/加价/取整）变更后，按新配置重算该连接已映射商品的本地售价。
// 由 Catalog Mapping 应用服务实现，通过 setter 注入以避免与本服务的循环依赖。
type MarkupReapplier interface {
	ReapplyMarkup(connectionID uint) (int, error)
}

// Service 对接连接服务。
type Service struct {
	connRepo        siteconnectioncontract.Repository
	encryptKey      []byte
	uploadsDir      string
	markupReapplier MarkupReapplier
	httpFactory     func(string, httpintegration.Config) (upstream.Adapter, error)
	httpValidator   func(httpintegration.Config) error
}

// NewService 创建连接服务。
func NewService(connRepo siteconnectioncontract.Repository, appSecretKey, uploadsDir string) *Service {
	return &Service{
		connRepo:   connRepo,
		encryptKey: crypto.DeriveKey(appSecretKey),
		uploadsDir: uploadsDir,
	}
}

// SetHTTPAdapter installs the production adapter without coupling application to transport.
func (s *Service) SetHTTPAdapter(factory func(string, httpintegration.Config) (upstream.Adapter, error), validate func(httpintegration.Config) error) {
	s.httpFactory, s.httpValidator = factory, validate
}

// SetMarkupReapplier 注入定价重算器（容器装配时调用）。
func (s *Service) SetMarkupReapplier(r MarkupReapplier) {
	s.markupReapplier = r
}

// Create 创建连接
func (s *Service) Create(input CreateInput) (*siteconnectiondomain.Connection, error) {
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.BaseURL) == "" {
		return nil, siteconnectioncontract.ErrInvalid
	}
	protocol := strings.TrimSpace(input.Protocol)
	if protocol == "" {
		protocol = constants.ConnectionProtocolDujiaoNext
	}
	switch protocol {
	case constants.ConnectionProtocolDujiaoNext:
		if strings.TrimSpace(input.ApiKey) == "" || strings.TrimSpace(input.ApiSecret) == "" {
			return nil, siteconnectioncontract.ErrInvalid
		}
	case constants.ConnectionProtocolGenericHTTP:
		// Generic credentials belong only to the encrypted HTTP configuration.
		input.ApiKey, input.ApiSecret = "", ""
	default:
		return nil, siteconnectioncontract.ErrProtocolUnsupported
	}

	encryptedSecret, err := crypto.Encrypt(s.encryptKey, input.ApiSecret)
	if err != nil {
		return nil, err
	}

	retryMax := input.RetryMax
	if retryMax <= 0 {
		retryMax = 5
	}
	retryIntervals := strings.TrimSpace(input.RetryIntervals)
	if retryIntervals == "" {
		retryIntervals = "[30,60,300]"
	}

	roundingMode := strings.TrimSpace(input.PriceRoundingMode)
	if roundingMode == "" {
		roundingMode = "none"
	}

	conn := &siteconnectiondomain.Connection{
		Name:               strings.TrimSpace(input.Name),
		BaseURL:            strings.TrimRight(strings.TrimSpace(input.BaseURL), "/"),
		ApiKey:             strings.TrimSpace(input.ApiKey),
		ApiSecret:          encryptedSecret,
		Protocol:           protocol,
		CallbackURL:        strings.TrimSpace(input.CallbackURL),
		Status:             constants.ConnectionStatusPending,
		RetryMax:           retryMax,
		RetryIntervals:     retryIntervals,
		ExchangeRate:       s.normalizeExchangeRate(input.ExchangeRate),
		PriceMarkupPercent: decimal.NewFromFloat(input.PriceMarkupPercent),
		PriceRoundingMode:  roundingMode,
		AutoSyncPrice:      input.AutoSyncPrice,
	}

	if err := s.connRepo.Create(conn); err != nil {
		return nil, err
	}
	return conn, nil
}

// Update 更新连接
func (s *Service) Update(id uint, input UpdateInput) (*siteconnectiondomain.Connection, error) {
	conn, err := s.connRepo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, siteconnectioncontract.ErrNotFound
	}

	if protocol := strings.TrimSpace(input.Protocol); protocol != "" && protocol != conn.Protocol {
		return nil, siteconnectioncontract.ErrProtocolImmutable
	}
	if conn.Protocol != constants.ConnectionProtocolDujiaoNext && conn.Protocol != constants.ConnectionProtocolGenericHTTP {
		return nil, siteconnectioncontract.ErrProtocolUnsupported
	}
	if conn.Protocol == constants.ConnectionProtocolGenericHTTP {
		input.ApiKey, input.ApiSecret = "", ""
		if input.BaseURL != "" && strings.TrimRight(strings.TrimSpace(input.BaseURL), "/") != conn.BaseURL {
			return nil, siteconnectioncontract.ErrInvalid
		}
	}

	// 记录定价配置旧值，用于判断本次保存是否需要重算已映射商品的本地售价。
	prevExchangeRate := conn.ExchangeRate
	prevMarkupPercent := conn.PriceMarkupPercent
	prevRoundingMode := conn.PriceRoundingMode

	if strings.TrimSpace(input.Name) != "" {
		conn.Name = strings.TrimSpace(input.Name)
	}
	if strings.TrimSpace(input.BaseURL) != "" {
		conn.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	}
	if strings.TrimSpace(input.ApiKey) != "" {
		conn.ApiKey = strings.TrimSpace(input.ApiKey)
	}
	if strings.TrimSpace(input.ApiSecret) != "" {
		encrypted, err := crypto.Encrypt(s.encryptKey, input.ApiSecret)
		if err != nil {
			return nil, err
		}
		conn.ApiSecret = encrypted
	}
	if strings.TrimSpace(input.Protocol) != "" {
		conn.Protocol = strings.TrimSpace(input.Protocol)
	}
	if input.CallbackURL != "" {
		conn.CallbackURL = strings.TrimSpace(input.CallbackURL)
	}
	if input.RetryMax > 0 {
		conn.RetryMax = input.RetryMax
	}
	if strings.TrimSpace(input.RetryIntervals) != "" {
		conn.RetryIntervals = strings.TrimSpace(input.RetryIntervals)
	}
	if input.ExchangeRate != nil {
		conn.ExchangeRate = s.normalizeExchangeRate(*input.ExchangeRate)
	}
	if input.PriceMarkupPercent != nil {
		conn.PriceMarkupPercent = decimal.NewFromFloat(*input.PriceMarkupPercent)
	}
	if input.PriceRoundingMode != nil {
		mode := strings.TrimSpace(*input.PriceRoundingMode)
		if mode == "" {
			mode = "none"
		}
		conn.PriceRoundingMode = mode
	}
	if input.AutoSyncPrice != nil {
		conn.AutoSyncPrice = *input.AutoSyncPrice
	}

	if err := s.connRepo.Update(conn); err != nil {
		return nil, err
	}

	// 定价配置（汇率/加价/取整）发生实际变化时，自动重算该连接已映射商品的本地售价，
	// 避免「改了汇率但已有商品价格不联动」。重算为尽力而为：失败不影响连接保存本身，
	// 仅记录告警，用户仍可通过「重新应用加价」手动补救。
	priceConfigChanged := !conn.ExchangeRate.Equal(prevExchangeRate) ||
		!conn.PriceMarkupPercent.Equal(prevMarkupPercent) ||
		conn.PriceRoundingMode != prevRoundingMode
	if priceConfigChanged && s.markupReapplier != nil {
		if _, err := s.markupReapplier.ReapplyMarkup(conn.ID); err != nil {
			logger.Warnw("reapply_markup_after_connection_update_failed",
				"connection_id", conn.ID, "error", err)
		}
	}

	return conn, nil
}

// Delete 删除连接
func (s *Service) Delete(id uint) error {
	conn, err := s.connRepo.GetByID(id)
	if err != nil {
		return err
	}
	if conn == nil {
		return siteconnectioncontract.ErrNotFound
	}
	return s.connRepo.Delete(id)
}

// GetByID 获取连接
func (s *Service) GetByID(id uint) (*siteconnectiondomain.Connection, error) {
	return s.connRepo.GetByID(id)
}

// List 列表查询
func (s *Service) List(filter siteconnectioncontract.ListFilter) ([]siteconnectiondomain.Connection, int64, error) {
	return s.connRepo.List(filter)
}

// SetStatus 设置连接状态
func (s *Service) SetStatus(id uint, status string) error {
	conn, err := s.connRepo.GetByID(id)
	if err != nil {
		return err
	}
	if conn == nil {
		return siteconnectioncontract.ErrNotFound
	}
	if conn.Protocol != constants.ConnectionProtocolDujiaoNext && conn.Protocol != constants.ConnectionProtocolGenericHTTP {
		return siteconnectioncontract.ErrProtocolUnsupported
	}
	if status != constants.ConnectionStatusActive && status != constants.ConnectionStatusDisabled && status != constants.ConnectionStatusPending {
		return siteconnectioncontract.ErrInvalid
	}
	if conn.Protocol == constants.ConnectionProtocolGenericHTTP {
		if status == constants.ConnectionStatusActive {
			cfg, err := s.loadHTTPConfig(conn)
			if err != nil {
				return err
			}
			if s.httpValidator == nil || s.httpFactory == nil || s.httpValidator(cfg) != nil {
				return siteconnectioncontract.ErrHTTPDraftOnly
			}
		}
		updated, err := s.connRepo.SetStatusIfRevision(id, conn.HTTPConfigRevision, status)
		if err != nil {
			return err
		}
		if !updated {
			return siteconnectioncontract.ErrHTTPConfigConflict
		}
		return nil
	}
	conn.Status = status
	return s.connRepo.Update(conn)
}

// Ping 测试连接
func (s *Service) Ping(id uint) (*PingResult, error) {
	conn, err := s.connRepo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, siteconnectioncontract.ErrNotFound
	}

	adapter, err := s.GetAdapter(conn)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, pingErr := adapter.Ping(ctx)
	now := time.Now()
	conn.LastPingAt = &now
	conn.LastPingOK = pingErr == nil

	if pingErr == nil && conn.Status == constants.ConnectionStatusPending && conn.Protocol == constants.ConnectionProtocolDujiaoNext {
		conn.Status = constants.ConnectionStatusActive
	}

	// 更新连接状态（不管 ping 是否成功）
	_ = s.connRepo.Update(conn)

	if pingErr != nil {
		return nil, pingErr
	}
	if result == nil {
		return nil, nil
	}
	return &PingResult{
		SiteName:        result.SiteName,
		ProtocolVersion: result.ProtocolVersion,
		UserID:          result.UserID,
		Balance:         result.Balance,
		Currency:        result.Currency,
		MemberLevel:     result.MemberLevel,
	}, nil
}

// GetAdapter 获取连接的适配器（解密 secret 后构建）
func (s *Service) GetAdapter(conn *siteconnectiondomain.Connection) (upstream.Adapter, error) {
	if conn != nil && conn.Protocol == constants.ConnectionProtocolGenericHTTP {
		cfg, err := s.loadHTTPConfig(conn)
		if err != nil {
			return nil, err
		}
		if s.httpFactory == nil || s.httpValidator == nil || s.httpValidator(cfg) != nil {
			return nil, siteconnectioncontract.ErrHTTPDraftOnly
		}
		return s.httpFactory(conn.BaseURL, cfg)
	}
	if err := automatedProtocolAllowed(conn); err != nil {
		return nil, err
	}
	decrypted, err := s.decryptSecret(conn)
	if err != nil {
		return nil, err
	}

	return upstream.NewAdapter(&siteconnectiondomain.Connection{
		BaseURL:   conn.BaseURL,
		ApiKey:    conn.ApiKey,
		ApiSecret: decrypted,
		Protocol:  conn.Protocol,
	}, s.uploadsDir)
}

// CaptureProcurementSnapshot is the procurement gateway's opaque snapshot port.
func (s *Service) CaptureProcurementSnapshot(id uint) (string, error) {
	conn, err := s.connRepo.GetByID(id)
	if err != nil {
		return "", err
	}
	if conn == nil {
		return "", siteconnectioncontract.ErrNotFound
	}
	if conn.Protocol != constants.ConnectionProtocolGenericHTTP {
		return "", nil
	}
	if conn.Status != constants.ConnectionStatusActive {
		return "", siteconnectioncontract.ErrHTTPDraftOnly
	}
	if _, err := s.GetAdapter(conn); err != nil {
		return "", err
	}
	return s.FreezeHTTPConnection(conn)
}

func (s *Service) OpenProcurementSnapshot(id uint, snapshot string) (*siteconnectiondomain.Connection, upstream.Adapter, error) {
	return s.RestoreHTTPConnection(id, snapshot)
}

// FreezeHTTPConnection captures the execution configuration without exposing plaintext.
// The caller persists this opaque value with the procurement, never in an API response.
func (s *Service) FreezeHTTPConnection(conn *siteconnectiondomain.Connection) (string, error) {
	if conn == nil || conn.Protocol != constants.ConnectionProtocolGenericHTTP {
		return "", siteconnectioncontract.ErrProtocolUnsupported
	}
	raw, err := json.Marshal(struct {
		ID       uint   `json:"id"`
		BaseURL  string `json:"base_url"`
		Revision uint64 `json:"revision"`
		Config   string `json:"encrypted_config"`
	}{conn.ID, conn.BaseURL, conn.HTTPConfigRevision, conn.HTTPConfigEncrypted})
	if err != nil {
		return "", siteconnectioncontract.ErrHTTPConfigInvalid
	}
	return crypto.Encrypt(s.encryptKey, string(raw))
}

func (s *Service) RestoreHTTPConnection(id uint, snapshot string) (*siteconnectiondomain.Connection, upstream.Adapter, error) {
	raw, err := crypto.Decrypt(s.encryptKey, snapshot)
	if err != nil {
		return nil, nil, siteconnectioncontract.ErrHTTPConfigInvalid
	}
	var frozen struct {
		ID       uint   `json:"id"`
		BaseURL  string `json:"base_url"`
		Revision uint64 `json:"revision"`
		Config   string `json:"encrypted_config"`
	}
	if json.Unmarshal([]byte(raw), &frozen) != nil || frozen.ID != id || frozen.Config == "" {
		return nil, nil, siteconnectioncontract.ErrHTTPConfigInvalid
	}
	conn, err := s.connRepo.GetByID(id)
	if err != nil {
		return nil, nil, err
	}
	if conn == nil {
		return nil, nil, siteconnectioncontract.ErrNotFound
	}
	if conn.Protocol != constants.ConnectionProtocolGenericHTTP {
		return nil, nil, siteconnectioncontract.ErrProtocolUnsupported
	}
	conn.BaseURL, conn.HTTPConfigEncrypted, conn.HTTPConfigRevision = frozen.BaseURL, frozen.Config, frozen.Revision
	adapter, err := s.GetAdapter(conn)
	return conn, adapter, err
}

func (s *Service) decryptSecret(conn *siteconnectiondomain.Connection) (string, error) {
	return crypto.Decrypt(s.encryptKey, conn.ApiSecret)
}

// DecryptSecret 解密加密后的 api_secret（公开方法，用于回调签名验证）
func (s *Service) DecryptSecret(encrypted string) (string, error) {
	return crypto.Decrypt(s.encryptKey, encrypted)
}

// normalizeExchangeRate 规范化汇率值，<=0 时返回 1
func (s *Service) normalizeExchangeRate(rate float64) decimal.Decimal {
	if rate <= 0 {
		return decimal.NewFromInt(1)
	}
	return decimal.NewFromFloat(rate)
}
