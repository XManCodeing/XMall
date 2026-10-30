package container

import (
	"context"
	"errors"
	telegrambootstrap "github.com/XManCodeing/XMall/internal/bootstrap/telegrambot"
	"github.com/XManCodeing/XMall/internal/bootstrap/telegrambroadcast"
	broadcastapp "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	eventstore "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/infrastructure/gormstore"
	runtimeapp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	runtimeapi "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/botapi"
	runtimestore "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	uploadlocal "github.com/XManCodeing/XMall/internal/modules/upload/infrastructure/localstore"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
	"time"
)

// initTelegramBot constructs dependencies only; polling belongs to BuildRunner.
func (c *Container) initTelegramBot() {
	store := runtimestore.New(gormdb.DB)
	c.TelegramBotStore = store
	c.TelegramBotManager = telegrambootstrap.NewManagerWithMedia(store, c.Config.App.SecretKey, uploadlocal.DefaultRoot)
	shopDependencies := telegrambootstrap.ShopDependencies{
		ContentSettings: c.SettingService,
		Store:           store, Secret: c.Config.App.SecretKey, Auth: c.UserAuthService, Users: c.UserStore,
		Categories: c.CategoryService, Products: c.ProductReadService, ProductRepo: c.ProductRepo,
		ProductMappings: c.ProductMappingRepo, SKUStocks: c.ProductMappingService,
		Orders: c.OrderService, Payments: c.PaymentService, Wallet: c.WalletService, Currency: c.SettingService.GetSiteCurrency,
	}
	shop := telegrambootstrap.NewShop(shopDependencies)
	broadcastBridge := &telegrambroadcast.RuntimeBridge{
		Store: store, Cipher: c.TelegramBotManager, UploadRoot: uploadlocal.DefaultRoot,
		Users:    telegrambroadcast.NewUserDirectory(c.ExternalIdentityStore),
		Identity: telegrambroadcast.ShopIdentity{Shop: shop},
	}
	c.TelegramBroadcastService = broadcastapp.NewRuntimeService(c.TelegramBroadcastRepo, broadcastBridge, broadcastBridge, telegrambroadcast.NewDispatcher(c.QueueClient))
	dispatcher := telegrambootstrap.NewBusinessDispatcher(eventstore.NewBusinessStore(gormdb.DB), shopDependencies, shop, store)
	if c.Config.Queue.Enabled {
		c.TelegramBotLeaseClient = telegrambootstrap.NewLeaseClient(c.Config.Queue)
		c.TelegramBotRuntime = runtimeapp.NewRuntime(c.TelegramBotManager, runtimeapi.Leaser{Client: c.TelegramBotLeaseClient}, shop)
		c.TelegramBotRuntime.Recover = func(ctx context.Context) error {
			return withRecoveryLease(ctx, runtimeapi.Leaser{Client: c.TelegramBotLeaseClient}, func(ctx context.Context) error {
				_, err := c.PaymentService.RecoverPaidFulfillment(ctx, 100)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.Join(err, c.TelegramBroadcastService.Reconcile(ctx))
			}, 8*time.Second, 3*time.Second, 25*time.Second)
		}
		c.TelegramBotRuntime.Reconcile = func(ctx context.Context, botID uint) error {
			if err := store.Cleanup(ctx, botID); err != nil {
				return err
			}
			return dispatcher.Reconcile(ctx, botID)
		}
	}
}
