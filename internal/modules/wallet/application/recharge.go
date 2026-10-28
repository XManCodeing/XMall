package application

import (
	"fmt"

	"github.com/XManCodeing/XMall/internal/constants"
	walletcontract "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
	walletdomain "github.com/XManCodeing/XMall/internal/modules/wallet/domain"

	"github.com/shopspring/decimal"
)

func (s *Service) ApplyRechargePayment(
	tx walletcontract.Transaction,
	recharge *walletdomain.RechargeOrder,
) (*walletdomain.Transaction, error) {
	if tx == nil {
		return nil, walletcontract.ErrTransactionRequired
	}
	if recharge == nil || recharge.ID == 0 || recharge.UserID == 0 {
		return nil, walletcontract.ErrRechargeNotFound
	}
	if recharge.Amount.Decimal.Round(2).LessThanOrEqual(decimal.Zero) {
		return nil, walletcontract.ErrInvalidAmount
	}
	_, transaction, err := s.CreditInTransaction(tx, walletcontract.CreditInput{
		UserID: recharge.UserID, Amount: recharge.Amount, Currency: recharge.Currency,
		Type:      constants.WalletTxnTypeRecharge,
		Reference: fmt.Sprintf("recharge:%d:success", recharge.ID),
		Remark:    cleanRemark(recharge.Remark, "在线充值到账"),
	})
	return transaction, err
}
