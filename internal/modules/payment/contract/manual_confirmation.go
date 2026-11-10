package contract

import (
	"errors"
	"time"

	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
)

var (
	ErrManualConfirmationInvalid     = errors.New("manual payment confirmation: invalid review evidence")
	ErrManualConfirmationConflict    = errors.New("manual payment confirmation: state or evidence conflict")
	ErrManualConfirmationUnavailable = errors.New("manual payment confirmation: transactional audit store unavailable")
)

// ManualConfirmationInput 是人工复核的完整事实。AdminID 必须由认证上下文注入，不能绑定请求 JSON。
// Amount 是支付尝试的外部实收金额（包括该尝试手续费），不是订单总额或钱包扣款金额。
// ConfirmTimeout 默认 false；超时复核必须明确为 true，也参与幂等摘要，旧普通请求摘要保持兼容。
type ManualConfirmationInput struct {
	ConfirmTimeout    bool      `json:"confirm_timeout,omitempty"`
	RequestID         string    `json:"request_id"`
	ExpectedStatus    string    `json:"expected_status"`
	Amount            string    `json:"amount"`
	Currency          string    `json:"currency"`
	PaidAt            time.Time `json:"paid_at"`
	TransactionID     string    `json:"transaction_id"`
	EvidenceReference string    `json:"evidence_reference"`
	Reason            string    `json:"reason"`
	AdminID           uint      `json:"-"`
}

// ManualConfirmationReview 仅返回安全的收款复核数据，不包含网关载荷、支付链接或用户凭证。
// Confirmation 为已持久化人工审计；没有人工审计的网关成功支付返回 null。
// RequiresTimeoutConfirmation 总是序列化为 boolean；只提示二次确认，不代表资源恢复必然成功。
type ManualConfirmationReview struct {
	PaymentID                   uint                              `json:"payment_id"`
	OrderID                     uint                              `json:"order_id"`
	OrderNo                     string                            `json:"order_no"`
	ChannelID                   uint                              `json:"channel_id"`
	ProviderType                string                            `json:"provider_type"`
	PaymentStatus               string                            `json:"payment_status"`
	OrderStatus                 string                            `json:"order_status"`
	Amount                      string                            `json:"amount"`
	Currency                    string                            `json:"currency"`
	OrderAmount                 string                            `json:"order_amount"`
	WalletPaidAmount            string                            `json:"wallet_paid_amount"`
	PaidAt                      *time.Time                        `json:"paid_at"`
	CreatedAt                   time.Time                         `json:"created_at"`
	ExpiresAt                   *time.Time                        `json:"expires_at"`
	TransactionID               string                            `json:"transaction_id"`
	ExceptionCode               string                            `json:"exception_code"`
	RequiresTimeoutConfirmation bool                              `json:"requires_timeout_confirmation"`
	CanConfirm                  bool                              `json:"can_confirm"`
	BlockedReason               string                            `json:"blocked_reason"`
	Confirmation                *paymentdomain.ManualConfirmation `json:"confirmation"`
	Replayed                    bool                              `json:"replayed"`
}

// ManualConfirmationService 是财务专用 GET/POST /admin/payments/:id/manual-confirmation 用例。
// GET 获取复核快照，POST 返回相同结构；成功仅指收款事务完成，不代表发货、邀请或入群。
type ManualConfirmationService interface {
	GetManualConfirmation(paymentID uint) (*ManualConfirmationReview, error)
	ConfirmManually(paymentID uint, input ManualConfirmationInput) (*ManualConfirmationReview, error)
}

// ManualConfirmationStore 必须绑定调用方事务。ClaimEvidence 为渠道内证据唯一占用；禁止全局 DB 回查。
type ManualConfirmationStore interface {
	FindManualConfirmation(paymentID uint) (*paymentdomain.ManualConfirmation, error)
	FindManualRequest(requestID string) (*paymentdomain.ManualConfirmation, error)
	CreateManualConfirmation(record *paymentdomain.ManualConfirmation) error
	ClaimEvidence(channelID uint, reference string, paymentID uint) error
	RecordReceiptAnomaly(record *paymentdomain.PaymentReceiptAnomaly) error
}
