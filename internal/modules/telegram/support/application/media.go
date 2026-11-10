package application

import (
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
)

// TransferReadyBuilder builds exactly the original content and current binding.
// Install store.WithReadyEncoder(contract.EncodeReadyTransfer) alongside it.
type TransferReadyBuilder struct{}

func (TransferReadyBuilder) BuildReady(prepared c.PreparedReady) (r.Message, error) {
	if len(prepared.Sources) > 0 {
		return c.EncodeReadyAlbum(prepared.Sources, prepared.Binding, c.ReadyBusinessRef(prepared.Message.SessionID, prepared.Message.ID))
	}
	return c.EncodeReadyTransfer(prepared.Source, prepared.Binding, c.ReadyBusinessRef(prepared.Message.SessionID, prepared.Message.ID))
}
