package credentialreader

import (
	"github.com/XManCodeing/XMall/internal/constants"
	apicredentialdomain "github.com/XManCodeing/XMall/internal/modules/apicredential/domain"
	downstreamcontract "github.com/XManCodeing/XMall/internal/modules/downstreamcallback/contract"
)

// Source 是 API 凭证上下文暴露给防腐适配器的最小端口。
type Source interface {
	GetByID(id uint) (*apicredentialdomain.ApiCredential, error)
	// GetByApiKey 预加载 User，与 upstream_auth 中间件读取同一份数据。
	GetByApiKey(apiKey string) (*apicredentialdomain.ApiCredential, error)
}

// Reader 将 API 凭证投影为下游回调签名凭证。
type Reader struct {
	source Source
}

var _ downstreamcontract.CredentialReader = (*Reader)(nil)

// New 创建凭证读取器；source 为空时 panic，属于装配错误。
func New(source Source) *Reader {
	if source == nil {
		panic("downstream callback credential reader: source is nil")
	}
	return &Reader{source: source}
}

// GetByID 读取凭证并计算 Active。
// 参数 id：API 凭证 ID。
// 返回：凭证投影；凭证不存在时返回 nil, nil。
func (r *Reader) GetByID(id uint) (*downstreamcontract.Credential, error) {
	credential, err := r.source.GetByID(id)
	if err != nil || credential == nil {
		return nil, err
	}
	withUser, err := r.source.GetByApiKey(credential.ApiKey)
	if err != nil {
		return nil, err
	}
	return &downstreamcontract.Credential{
		ID:        credential.ID,
		APIKey:    credential.ApiKey,
		APISecret: credential.ApiSecret,
		Active:    isCredentialActive(withUser),
	}, nil
}

// isCredentialActive 与 API 鉴权同一标准：已审批、已启用且所属用户未禁用。
func isCredentialActive(credential *apicredentialdomain.ApiCredential) bool {
	return credential != nil &&
		credential.Status == constants.ApiCredentialStatusApproved &&
		credential.IsActive &&
		credential.User != nil &&
		credential.User.Status == constants.UserStatusActive
}
