-- Synthetic fresh v9 SQLite baseline; NOT a production dump or all pre-v9 upgrade histories.
-- Source commit: 75379d7cfa87e5711471b07d3ae311584d477df4
-- Named foreign keys sorted; synthetic migration timestamps normalized; data rows ordered.
-- Source hashes describe compiled slices: reseller migration only; orderrisk NormalizeRiskIP only.
-- sha256 dd974eac27d4933415ad49f11dc6ee7c39a8faddedcdc3a804790de870ca3d69 internal/bootstrap/database/migrations/migrations.go
-- sha256 dba82d2794834bb445c3f33362e3133320255d0ea80401985d2a7c7dd8104638 internal/bootstrap/database/migrations/registry.go
-- sha256 e8c48aa6636fad097a0d2eec24c62602a919225a8ced286fb1b6cd976306c91b internal/constants/admin_login_event.go
-- sha256 36154a492dba4ec1bbadf6a12b2c7496b1b82b30b0204c1c344241ca82bb5cf3 internal/constants/constants.go
-- sha256 d280737d7c9c3cc96b1827871b20588f4b6c14211b2efdb2fae727ac6754021c internal/modules/affiliate/domain/click.go
-- sha256 e10cec3d8f51b818209d683c04836f1dee534ea323a81c246282d96e6976fab9 internal/modules/affiliate/domain/commission.go
-- sha256 8486c81bb9dde489cdd95a566c091620bd7cea5339b3cd2c18779df0e2815248 internal/modules/affiliate/domain/order_reference.go
-- sha256 4ed82442a0e02fdf02b2f735010edcf84ac6cfb09487797695b80557e7b7aaed internal/modules/affiliate/domain/profile.go
-- sha256 67c4d8fc4a76c0154e0a8895ebb2bdc1076162d4f0715d8a2a921a267259288e internal/modules/affiliate/domain/withdraw_request.go
-- sha256 f983835378323c25d23776961e8fbc556ee20d9f062e0be3ce75d8715ec3d708 internal/modules/apicredential/domain/credential.go
-- sha256 6ffacaca2af74903d9bbdec9caad78279624106a5aafdcb274eeaff0ef1f8547 internal/modules/auditlog/domain/admin_login.go
-- sha256 34c7b42c9426415feaf9f2bd88d2f22c892d4cee8078106fe3bf2376392a8e4e internal/modules/auditlog/domain/authz.go
-- sha256 522a0f6b4cc12d83994c6ec17c40c4436ebfca656db3a3e82fd142a200e85d1e internal/modules/auditlog/domain/user_login.go
-- sha256 756f45501a088da4e44600195e3b9a84897e80e1e639f8ec3c86555a5c4b7785 internal/modules/cardsecret/domain/batch.go
-- sha256 96540a3998a36cbf21951e2a73534a63aa5012d0e0f436125c9185d8e3d84bab internal/modules/cardsecret/domain/secret.go
-- sha256 8c474ba42024dcf4484c23a4cb0ac9cff8420e52495f73a29df8ee4a4917a0ae internal/modules/cart/domain/item.go
-- sha256 c342ceb8c3790693da6c707ef2019c3aabcf471c1e5abd245d82c888096fe941 internal/modules/catalog/category/domain/category.go
-- sha256 261ef316d1f49912b9a1a8902f4e203e6e66c1cf9dbd8e698642f75a9da2808e internal/modules/catalog/mapping/domain/mapping.go
-- sha256 fc5446de50bd3fbcb07dc826c5f329274dbd47960fb54e8e37c706d6d3b3b543 internal/modules/catalog/product/domain/inventory_policy.go
-- sha256 476fb1ed0fd64e589ed9578edcf090518cb22ab166f6e2f6eda71d9849f2a699 internal/modules/catalog/product/domain/payment_channels.go
-- sha256 e4c91f62fddb4aa091d9c4cc4aee24650a3cd58730c3b63dbbe543858eed47fb internal/modules/catalog/product/domain/pricing.go
-- sha256 5b330de5e89193c3e5935a76618be1a605582a991768e5329247c459e7eeca17 internal/modules/catalog/product/domain/product.go
-- sha256 dff3b81f93e3e597aae3dcfb5e1bfa14162abd21ddec328b2e5556fbf8ef9274 internal/modules/catalog/product/domain/purchase_policy.go
-- sha256 7e5f8435676a1b91173c95ae82277cd34fe9de29a409089a74fc3314c840aa9e internal/modules/catalog/product/domain/sku.go
-- sha256 95b338fe93731c1ec5b696706ed39f19802c2c3b50babb6cbd4a45cec25b031a internal/modules/catalog/product/domain/wholesale_price.go
-- sha256 429bb4e9c0d9ce619fcfa0d45b98bc64622c4de98ed5af7c779cf9a3de157b82 internal/modules/channelclient/domain/client.go
-- sha256 153984792c947a3fcc9739d5249dc5fc8d2a5cc783cecfd29f036474bfe7597a internal/modules/content/domain/banner.go
-- sha256 0da49cee93f67f6bca76551602e0cb8cb40eedf8bdf2b9c6d549cc68ed4229d7 internal/modules/content/domain/media.go
-- sha256 4572cbb99e7069cc11ff28890c0a342e59f7cbae5ae60fdaddef85dff46fd9ee internal/modules/content/domain/post.go
-- sha256 631c8e5707e23492a27629b2f651bcf9ba04a60f443b8e6629bb70625a70a242 internal/modules/content/domain/post_category.go
-- sha256 c46aa553db6a1401f2f3eb772e66c5a24bcf29a98f6dbe00a8b3adafdceb0979 internal/modules/coupon/domain/coupon.go
-- sha256 ca0abf2d18263397343f22a9da8fa9056db5c21e4e0895a2c3ce2604621abff0 internal/modules/coupon/domain/scope.go
-- sha256 b14a37b5a0eaf3c6bd0a6157972308f48e23d659a6bd58eb94aa9441a9bb57f4 internal/modules/coupon/domain/usage.go
-- sha256 776e0035d049ceaf1d4bf6b6a7032ee46e090d054f97a071642915f7c7cc8634 internal/modules/downstreamcallback/domain/order_ref.go
-- sha256 728be67bfb282d74dd1c134cc7850e4294335cd94f2b24b85f6783ed417d4a97 internal/modules/fulfillment/domain/fulfillment.go
-- sha256 74f61a8a3a91103444c2b19a5d396c33c9fd78f51686992cbcd1a3437488b7d8 internal/modules/giftcard/domain/batch.go
-- sha256 91e21357b8dbc7dbb95e0cbfe716e09365ec0e679d2ee3703f91e18d603598d4 internal/modules/giftcard/domain/card.go
-- sha256 3978564c4038eca94ebb68afeafad709e73e35367d802a3f3467c4116e516dc4 internal/modules/identity/admin/domain/admin.go
-- sha256 06e16096a1f1db189aebd902a197419174346ad7b557c4fa1f240fb73a12b63b internal/modules/identity/emailverification/domain/code.go
-- sha256 c659d290fae51f18a7cc8907dc671092be3e819640d07cfb2fc1b48dbc217716 internal/modules/identity/externalidentity/domain/identity.go
-- sha256 9cb0ff506ef1eb1308b296ee90f97b3d947c65b30795edba1ab80a571f33518b internal/modules/identity/invitecode/domain/invite_code.go
-- sha256 3202a96316ec9459435376684cc12ed7ba7edad229300e751740d15eaf5260fd internal/modules/identity/user/domain/user.go
-- sha256 bac9933ad172cc4b3eff2c98014770e58ef04da5a8980de50295ac8bdf3312d1 internal/modules/identity/userauth/domain/telegram_history.go
-- sha256 978b70e525fd0221a1ac78ba616e8428c287a01c234f60174bb8958ef00c743e internal/modules/memberlevel/domain/level.go
-- sha256 dc67921a90d8e4bb2cffd8025c4a8c90821ee80ab382c777c432ff2eb45c49f5 internal/modules/memberlevel/domain/price.go
-- sha256 5c3d63fb42b9ae6e171b0ebcbc2435a4ce0da1cdc221c9083c7c575ca3d351f9 internal/modules/notification/domain/log.go
-- sha256 731411f59cb3ceb5c3387cff3ee983daa0bd5822f2d8941a361fa057c2130105 internal/modules/order/domain/item_key.go
-- sha256 6598cb15a8c97aa6cbfa1bf5da5b7cae42314a984ddd65abad5d4f2bebd2283d internal/modules/order/domain/order.go
-- sha256 4a627965beda99045df99660ae3f40a70dd77d9ad50a6e4f99875038dcb3cf1d internal/modules/order/domain/order_item.go
-- sha256 f04e229a96bbc5659abd05664127aeb613a20b764fa872e32754132b05abc956 internal/modules/order/domain/order_refund_record.go
-- sha256 5de0cf7b9f9f80fe8f4356f60789f8b1a6ab4daf529c06ace000f1624c3eab25 internal/modules/orderrisk/contract/types.go
-- sha256 a97a67047f8020869575e5de27b22a6a5abfef5cb7e9f785a6ba27889da75df8 internal/modules/orderrisk/domain/lock_key.go
-- sha256 c77681c8d21d58f7aad7f258e43d1e54f345b89bed0c59101db4105b23594c29 internal/modules/payment/domain/channel.go
-- sha256 2014588655c6f4af4305f311daa6028d198b060d4ea5def5faac8c8d8ef8b336 internal/modules/payment/domain/payment.go
-- sha256 96b14358ce6320a9539adf4a6d93144fb9ca6e8decdd20c15beb5bfcd730fb92 internal/modules/procurement/domain/order.go
-- sha256 3461fd503e134d0739a2741337d00f0155e0dfe4e3d52a0f27f4019baf8838e2 internal/modules/promotion/domain/promotion.go
-- sha256 a31aa7e2867beb2d7a43c0eb52b84216fc86bd834b6cd23bfdf5ee2cee1556a5 internal/modules/reconciliation/domain/item.go
-- sha256 6d5fc08f3364f8e74d62812c077faa48fc5daa392a3630a93ba66a660db8d5ed internal/modules/reconciliation/domain/job.go
-- sha256 9972e159d63ad410a49c0d22a5047c5c875ccab29de81a9bab118d3db868926d internal/modules/reconciliation/domain/status.go
-- sha256 f58762b96de362cbb7ed79294219f4a15044e1ac851fc482a5ff0f6e16775bc9 internal/modules/reseller/domain/accounting.go
-- sha256 0d8c59c75c40f26d8c1291fe0f95129443760dc47aa0078634524a65793e8ffa internal/modules/reseller/domain/constants.go
-- sha256 834554db90b0bd5fc33d9487c7348e79923b36f7312141fa6d410f4efcb46089 internal/modules/reseller/domain/order.go
-- sha256 7dcdafe571844da1fd7f17f70fd119c90335af0bc34d2eceb19260c976afcea4 internal/modules/reseller/domain/product_setting.go
-- sha256 b195e618276afa558bcfd36c9fbed274d85f5d08b66f30ef2b7c01f3485215bb internal/modules/reseller/domain/profile.go
-- sha256 e16346da053ce10aad676e89df13bc7868ecdd16e58573d0dcec4e793172457c internal/modules/reseller/domain/related_account.go
-- sha256 083fe755f2739e78d1a7be50608d42747d5e072a42f51d79da7a8f6e771ec4ce internal/modules/reseller/domain/site.go
-- sha256 4770c644b91d8a5c05f7bfacef443fa9ba110e8d1d6b6c6b3a171c55bc8d2902 internal/modules/reseller/infrastructure/gormstore/store.go
-- sha256 e48d6ade43a21079904329844e00c68288296058548c0f27e9d355717ab5f598 internal/modules/settings/infrastructure/gormstore/store.go
-- sha256 ae8060cf79102959ac19e936bbaf0c30a0c79a2486d82e8aefe5ad2a355b94c7 internal/modules/settings/infrastructure/gormstore/telegram_bot_cas.go
-- sha256 bf495c61cf14c436cf2ff5ea6e2152f751e14e192b028c1e20ba77f06805af65 internal/modules/siteconnection/domain/connection.go
-- sha256 c58f86c61f3d39d08149d7cfc315af3667bcdf7aa21758d78bfd9ae62167cae2 internal/modules/telegram/broadcast/domain/broadcast.go
-- sha256 828738c7c1e4e68c1bbbae3d278dc6bd5287a9b3a552bac44ddc6df3aecff010 internal/modules/telegram/delivery/contract/business_events.go
-- sha256 c0bb403b0672189c776ab6da17d717489a14294958bcbd67acb85a1ae63cd9de internal/modules/telegram/delivery/domain/business_event.go
-- sha256 cb50b93756261af63d1cf90ca76df23c307039664c1909f3e0d0790335425aa9 internal/modules/telegram/runtime/domain/delivery_retry.go
-- sha256 6827d25b03398cb47169263b5c7cba23cd81f39254f82a8802d4de9164d72271 internal/modules/telegram/runtime/domain/media_cache.go
-- sha256 0a3f0b40356431bfb4cbb992b40d87d6cbff0fa76d7f21b7e2e2697e9ee5beb2 internal/modules/telegram/runtime/domain/models.go
-- sha256 ebfc052fd4e14697edaed4afbc9089b86456e847f4755d089a9a7da68122d1cf internal/modules/wallet/domain/account.go
-- sha256 fbe9cf22459894f67ff64c4a0f7893e88029cb5b1de7e33e057d4695e3c518eb internal/modules/wallet/domain/recharge_order.go
-- sha256 ff785ba8fd189716d80c205d457ca289a75a4148894060da6ceeb2000324049a internal/modules/wallet/domain/transaction.go
-- sha256 424182c13d7d78f0dcfe356aa045bc84dd9d1a709b3c3fe367e09770f911dff7 internal/platform/database/gormdb/db.go
-- sha256 393430008953fc9d769535e606fb964a1cfaf1d7523accb8b2f9a334d78d42a3 internal/shared/jsonmap/json.go
-- sha256 5d3e3fafb121d235d780844b424f98e8c2215e5a03559f05030925233e14d3a0 internal/shared/jsonslice/jsonslice.go
-- sha256 9e07d99cf49f0f8df6c08c27748d959769284c5007d5c773daf31fa74cfcdc06 internal/shared/money/amount.go
CREATE TABLE `admin_login_logs` (`id` integer PRIMARY KEY AUTOINCREMENT,`admin_id` integer,`username` text NOT NULL DEFAULT "",`event_type` varchar(40) NOT NULL DEFAULT "",`status` text NOT NULL,`fail_reason` text NOT NULL DEFAULT "",`client_ip` varchar(64),`user_agent` text,`request_id` varchar(64),`operator_id` integer,`created_at` datetime);
CREATE TABLE `admins` (`id` integer PRIMARY KEY AUTOINCREMENT,`username` text NOT NULL,`password_hash` text NOT NULL,`token_version` integer NOT NULL DEFAULT 0,`token_invalid_before` datetime,`is_super` numeric NOT NULL DEFAULT false,`last_login_at` datetime,`totp_secret` varchar(512) DEFAULT "",`totp_enabled_at` datetime,`totp_pending_secret` varchar(512) DEFAULT "",`totp_pending_expires_at` datetime,`recovery_codes` text,`created_at` datetime,`deleted_at` datetime);
CREATE TABLE `affiliate_clicks` (`id` integer PRIMARY KEY AUTOINCREMENT,`affiliate_profile_id` integer NOT NULL,`visitor_key` varchar(128),`landing_path` varchar(512),`referrer` varchar(1024),`client_ip` varchar(64),`user_agent` varchar(1024),`created_at` datetime NOT NULL,CONSTRAINT `fk_affiliate_clicks_affiliate_profile` FOREIGN KEY (`affiliate_profile_id`) REFERENCES `affiliate_profiles`(`id`));
CREATE TABLE `affiliate_commissions` (`id` integer PRIMARY KEY AUTOINCREMENT,`affiliate_profile_id` integer NOT NULL,`order_id` integer NOT NULL,`order_item_id` integer,`commission_type` varchar(20) NOT NULL DEFAULT "order",`base_amount` decimal(20,2) NOT NULL DEFAULT "0",`rate_percent` decimal(10,2) NOT NULL DEFAULT "0",`commission_amount` decimal(20,2) NOT NULL DEFAULT "0",`status` varchar(32) NOT NULL,`confirm_at` datetime,`available_at` datetime,`withdraw_request_id` integer,`invalid_reason` varchar(255),`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_affiliate_commissions_affiliate_profile` FOREIGN KEY (`affiliate_profile_id`) REFERENCES `affiliate_profiles`(`id`),CONSTRAINT `fk_affiliate_commissions_order` FOREIGN KEY (`order_id`) REFERENCES `orders`(`id`),CONSTRAINT `fk_affiliate_commissions_withdraw_request` FOREIGN KEY (`withdraw_request_id`) REFERENCES `affiliate_withdraw_requests`(`id`));
CREATE TABLE `affiliate_profiles` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL,`affiliate_code` varchar(32) NOT NULL,`status` varchar(20) NOT NULL,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_affiliate_profiles_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`));
CREATE TABLE `affiliate_withdraw_requests` (`id` integer PRIMARY KEY AUTOINCREMENT,`affiliate_profile_id` integer NOT NULL,`amount` decimal(20,2) NOT NULL DEFAULT "0",`channel` varchar(50) NOT NULL,`account` varchar(255) NOT NULL,`status` varchar(32) NOT NULL,`reject_reason` varchar(255),`processed_by` integer,`processed_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_affiliate_withdraw_requests_affiliate_profile` FOREIGN KEY (`affiliate_profile_id`) REFERENCES `affiliate_profiles`(`id`),CONSTRAINT `fk_affiliate_withdraw_requests_processor` FOREIGN KEY (`processed_by`) REFERENCES `admins`(`id`));
CREATE TABLE `api_credentials` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL,`api_key` varchar(64),`api_secret` varchar(256),`status` varchar(20) NOT NULL DEFAULT "pending_review",`reject_reason` varchar(500),`approved_at` datetime,`is_active` numeric NOT NULL DEFAULT false,`last_used_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_api_credentials_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`));
CREATE TABLE `authz_audit_logs` (`id` integer PRIMARY KEY AUTOINCREMENT,`operator_admin_id` integer NOT NULL,`operator_username` varchar(100) NOT NULL DEFAULT "",`target_admin_id` integer,`target_username` varchar(100) NOT NULL DEFAULT "",`action` varchar(100) NOT NULL,`role` varchar(120) NOT NULL DEFAULT "",`object` varchar(255) NOT NULL DEFAULT "",`method` varchar(20) NOT NULL DEFAULT "",`request_id` varchar(64) NOT NULL DEFAULT "",`detail_json` json,`created_at` datetime);
CREATE TABLE `banners` (`id` integer PRIMARY KEY AUTOINCREMENT,`name` varchar(120) NOT NULL,`position` varchar(60) NOT NULL,`title_json` json,`subtitle_json` json,`image` varchar(500) NOT NULL,`mobile_image` varchar(500),`link_type` varchar(20) NOT NULL DEFAULT "none",`link_value` varchar(1000),`open_in_new_tab` numeric DEFAULT false,`is_active` numeric DEFAULT true,`start_at` datetime,`end_at` datetime,`sort_order` integer DEFAULT 0,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `card_secret_batches` (`id` integer PRIMARY KEY AUTOINCREMENT,`product_id` integer NOT NULL,`sku_id` integer NOT NULL DEFAULT 0,`batch_no` text NOT NULL,`source` text NOT NULL,`total_count` integer NOT NULL,`note` text,`created_by` integer,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `card_secrets` (`id` integer PRIMARY KEY AUTOINCREMENT,`product_id` integer NOT NULL,`sku_id` integer NOT NULL DEFAULT 0,`batch_id` integer,`secret` text NOT NULL,`status` text NOT NULL,`order_id` integer,`reserved_at` datetime,`used_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_card_secrets_batch` FOREIGN KEY (`batch_id`) REFERENCES `card_secret_batches`(`id`));
CREATE TABLE "cart_items"  (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL,`product_id` integer NOT NULL,`sku_id` integer NOT NULL DEFAULT 0,`quantity` integer NOT NULL,`fulfillment_type` varchar(20) NOT NULL DEFAULT "manual",`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_cart_items_product` FOREIGN KEY (`product_id`) REFERENCES `products`(`id`),CONSTRAINT `fk_cart_items_sku` FOREIGN KEY (`sku_id`) REFERENCES `product_skus`(`id`));
CREATE TABLE `categories` (`id` integer PRIMARY KEY AUTOINCREMENT,`parent_id` integer NOT NULL DEFAULT 0,`slug` text NOT NULL,`name_json` json NOT NULL,`icon` varchar(500),`sort_order` integer DEFAULT 0,`is_active` numeric NOT NULL DEFAULT true,`created_at` datetime,`deleted_at` datetime);
CREATE TABLE `channel_clients` (`revision` integer NOT NULL DEFAULT 1,`id` integer PRIMARY KEY AUTOINCREMENT,`name` text NOT NULL,`channel_type` text NOT NULL,`channel_key` text NOT NULL,`channel_secret` text NOT NULL,`bot_token` text,`callback_url` text,`status` integer NOT NULL DEFAULT 1,`description` text,`last_used_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `coupon_usages` (`id` integer PRIMARY KEY AUTOINCREMENT,`coupon_id` integer NOT NULL,`user_id` integer NOT NULL,`order_id` integer NOT NULL,`discount_amount` decimal(20,2) NOT NULL DEFAULT "0",`created_at` datetime,`deleted_at` datetime);
CREATE TABLE `coupons` (`id` integer PRIMARY KEY AUTOINCREMENT,`code` text NOT NULL,`type` text NOT NULL,`value` decimal(20,2) NOT NULL,`min_amount` decimal(20,2) NOT NULL DEFAULT "0",`max_discount` decimal(20,2) NOT NULL DEFAULT "0",`usage_limit` integer NOT NULL DEFAULT 0,`used_count` integer NOT NULL DEFAULT 0,`per_user_limit` integer NOT NULL DEFAULT 0,`disabled_wholesale_price` numeric NOT NULL DEFAULT false,`per_item_discount` numeric NOT NULL DEFAULT false,`payment_roles` json,`member_levels` json,`scope_type` text NOT NULL,`scope_ref_ids` text,`starts_at` datetime,`ends_at` datetime,`is_active` numeric NOT NULL DEFAULT true,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `downstream_order_refs` (`id` integer PRIMARY KEY AUTOINCREMENT,`order_id` integer NOT NULL,`api_credential_id` integer NOT NULL,`downstream_order_no` varchar(64),`callback_url` varchar(500),`trace_id` varchar(64),`callback_status` varchar(20) NOT NULL DEFAULT "pending",`callback_retry_count` integer NOT NULL DEFAULT 0,`last_callback_at` datetime,`created_at` datetime,`updated_at` datetime);
CREATE TABLE `email_verify_codes` (`id` integer PRIMARY KEY AUTOINCREMENT,`email` text NOT NULL,`user_id` integer,`purpose` text NOT NULL,`code` text NOT NULL,`expires_at` datetime,`verified_at` datetime,`attempt_count` integer DEFAULT 0,`sent_at` datetime,`created_at` datetime,`deleted_at` datetime);
CREATE TABLE `fulfillments` (`id` integer PRIMARY KEY AUTOINCREMENT,`order_id` integer NOT NULL,`type` text NOT NULL,`status` text NOT NULL,`payload` text,`logistics_json` json,`delivered_by` integer,`delivered_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_orders_fulfillment` FOREIGN KEY (`order_id`) REFERENCES `orders`(`id`));
CREATE TABLE `gift_card_batches` (`id` integer PRIMARY KEY AUTOINCREMENT,`batch_no` varchar(48) NOT NULL,`name` varchar(120) NOT NULL,`amount` decimal(20,2) NOT NULL,`currency` varchar(16) NOT NULL DEFAULT "CNY",`quantity` integer NOT NULL DEFAULT 0,`expires_at` datetime,`created_by` integer,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `gift_cards` (`id` integer PRIMARY KEY AUTOINCREMENT,`batch_id` integer,`name` varchar(120) NOT NULL,`code` varchar(80) NOT NULL,`amount` decimal(20,2) NOT NULL,`currency` varchar(16) NOT NULL DEFAULT "CNY",`status` varchar(24) NOT NULL DEFAULT "active",`expires_at` datetime,`redeemed_at` datetime,`redeemed_user_id` integer,`wallet_txn_id` integer,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_gift_card_batches_cards` FOREIGN KEY (`batch_id`) REFERENCES `gift_card_batches`(`id`) ON UPDATE CASCADE);
CREATE TABLE `invite_codes` (`id` integer PRIMARY KEY AUTOINCREMENT,`code` varchar(32) NOT NULL,`max_uses` integer NOT NULL DEFAULT 1,`used_count` integer NOT NULL DEFAULT 0,`expires_at` datetime,`status` varchar(16) NOT NULL DEFAULT "active",`note` varchar(255) DEFAULT "",`created_by` integer,`created_at` datetime,`updated_at` datetime);
CREATE TABLE `media` (`id` integer PRIMARY KEY AUTOINCREMENT,`name` varchar(255) NOT NULL,`filename` varchar(255) NOT NULL,`path` varchar(500) NOT NULL,`mime_type` varchar(100) NOT NULL,`size` integer NOT NULL,`scene` varchar(60) NOT NULL,`width` integer NOT NULL DEFAULT 0,`height` integer NOT NULL DEFAULT 0,`created_at` datetime,`deleted_at` datetime);
CREATE TABLE `member_level_prices` (`id` integer PRIMARY KEY AUTOINCREMENT,`member_level_id` integer NOT NULL,`product_id` integer NOT NULL,`sku_id` integer NOT NULL DEFAULT 0,`price_amount` decimal(20,2) NOT NULL DEFAULT "0",`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `member_levels` (`id` integer PRIMARY KEY AUTOINCREMENT,`name_json` json NOT NULL,`slug` text NOT NULL,`icon` text DEFAULT "",`discount_rate` decimal(6,2) NOT NULL DEFAULT "100",`recharge_threshold` decimal(20,2) NOT NULL DEFAULT "0",`spend_threshold` decimal(20,2) NOT NULL DEFAULT "0",`is_default` numeric NOT NULL DEFAULT false,`sort_order` integer NOT NULL DEFAULT 0,`is_active` numeric NOT NULL DEFAULT true,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `notification_logs` (`id` integer PRIMARY KEY AUTOINCREMENT,`event_type` varchar(100) NOT NULL DEFAULT "",`biz_type` varchar(100) NOT NULL DEFAULT "",`biz_id` integer NOT NULL DEFAULT 0,`channel` varchar(32) NOT NULL DEFAULT "",`recipient` varchar(255) NOT NULL DEFAULT "",`locale` varchar(20) NOT NULL DEFAULT "",`title` text NOT NULL,`body` text NOT NULL,`status` varchar(32) NOT NULL DEFAULT "",`error_message` text,`is_test` numeric NOT NULL DEFAULT false,`variables_json` json,`created_at` datetime);
CREATE TABLE `order_items` (`id` integer PRIMARY KEY AUTOINCREMENT,`order_id` integer NOT NULL,`product_id` integer NOT NULL,`sku_id` integer NOT NULL DEFAULT 0,`title_json` json NOT NULL,`sku_snapshot_json` json,`tags` json,`original_unit_price` decimal(20,2) NOT NULL DEFAULT "0",`unit_price` decimal(20,2) NOT NULL DEFAULT "0",`cost_price` decimal(20,2) NOT NULL DEFAULT "0",`quantity` integer NOT NULL,`original_total_price` decimal(20,2) NOT NULL DEFAULT "0",`total_price` decimal(20,2) NOT NULL DEFAULT "0",`coupon_discount` decimal(20,2) NOT NULL DEFAULT "0",`member_discount` decimal(20,2) NOT NULL DEFAULT "0",`promotion_discount` decimal(20,2) NOT NULL DEFAULT "0",`wholesale_discount` decimal(20,2) NOT NULL DEFAULT "0",`promotion_id` integer,`fulfillment_type` text NOT NULL,`manual_form_schema_snapshot_json` json,`manual_form_submission_json` json,`instructions_json` json,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_orders_items` FOREIGN KEY (`order_id`) REFERENCES `orders`(`id`));
CREATE TABLE `order_refund_records` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL DEFAULT 0,`guest_email` varchar(255),`order_id` integer NOT NULL,`type` varchar(32) NOT NULL,`amount` decimal(20,2) NOT NULL DEFAULT "0",`payment_fee_refunded` numeric NOT NULL DEFAULT false,`payment_fee_refunded_amount` decimal(20,2) NOT NULL DEFAULT "0",`currency` varchar(16) NOT NULL DEFAULT "",`remark` text,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `order_risk_lock_keys` (`key_hash` varchar(64),`created_at` datetime NOT NULL,PRIMARY KEY (`key_hash`));
CREATE TABLE "orders"  (`id` integer PRIMARY KEY AUTOINCREMENT,`order_no` text,`parent_id` integer,`user_id` integer NOT NULL,`guest_email` text,`guest_password` varchar(200),`guest_locale` varchar(20),`status` text NOT NULL,`currency` text NOT NULL,`original_amount` decimal(20,2) NOT NULL DEFAULT "0",`discount_amount` decimal(20,2) NOT NULL DEFAULT "0",`member_discount_amount` decimal(20,2) NOT NULL DEFAULT "0",`promotion_discount_amount` decimal(20,2) NOT NULL DEFAULT "0",`wholesale_discount_amount` decimal(20,2) NOT NULL DEFAULT "0",`total_amount` decimal(20,2) NOT NULL DEFAULT "0",`wallet_paid_amount` decimal(20,2) NOT NULL DEFAULT "0",`online_paid_amount` decimal(20,2) NOT NULL DEFAULT "0",`refunded_amount` decimal(20,2) NOT NULL DEFAULT "0",`member_level_id` integer,`coupon_id` integer,`promotion_id` integer,`affiliate_profile_id` integer,`affiliate_code` varchar(32),`reseller_id` integer,`reseller_domain` varchar(255),`reseller_profit_amount` decimal(20,2) NOT NULL DEFAULT "0",`client_ip` varchar(64),`risk_ip` varchar(80),`expires_at` datetime,`paid_at` datetime,`canceled_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_orders_children` FOREIGN KEY (`parent_id`) REFERENCES `orders`(`id`));
CREATE TABLE `payment_channels` (`id` integer PRIMARY KEY AUTOINCREMENT,`name` text NOT NULL,`icon` varchar(512) DEFAULT "",`provider_type` text NOT NULL,`channel_type` text NOT NULL,`interaction_mode` text NOT NULL,`fee_rate` decimal(6,2) NOT NULL DEFAULT "0",`fixed_fee` decimal(6,2) NOT NULL DEFAULT "0",`min_amount` decimal(20,2) NOT NULL DEFAULT "0",`max_amount` decimal(20,2) NOT NULL DEFAULT "0",`hide_amount_out_range` numeric NOT NULL DEFAULT false,`payment_roles` json,`member_levels` json,`payment_types` json,`config_json` json,`is_active` numeric NOT NULL DEFAULT true,`sort_order` integer NOT NULL DEFAULT 0,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `payments` (`operation_key` text,`operation_hash` text,`creation_state` text,`source_bot_id` integer,`source_telegram_id` integer,`source_private_chat_id` integer,`id` integer PRIMARY KEY AUTOINCREMENT,`order_id` integer NOT NULL,`channel_id` integer NOT NULL,`provider_type` text NOT NULL,`channel_type` text NOT NULL,`interaction_mode` text NOT NULL,`amount` decimal(20,2) NOT NULL,`fee_rate` decimal(6,2) NOT NULL DEFAULT "0",`fixed_fee` decimal(6,2) NOT NULL DEFAULT "0",`fee_amount` decimal(20,2) NOT NULL DEFAULT "0",`fee_policy` varchar(32) NOT NULL DEFAULT "none",`currency` text NOT NULL,`status` text NOT NULL,`exception_code` varchar(64),`provider_ref` text,`gateway_order_no` text,`provider_payload` json,`pay_url` text,`qr_code` text,`created_at` datetime,`updated_at` datetime,`paid_at` datetime,`expired_at` datetime,`superseded_at` datetime,`superseded_by_payment_id` integer,`callback_at` datetime,`deleted_at` datetime);
CREATE TABLE `post_categories` (`id` integer PRIMARY KEY AUTOINCREMENT,`parent_id` integer,`slug` text NOT NULL,`name_json` json NOT NULL,`icon` varchar(500),`is_active` numeric NOT NULL DEFAULT true,`sort_order` integer DEFAULT 0,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_post_categories_children` FOREIGN KEY (`parent_id`) REFERENCES `post_categories`(`id`));
CREATE TABLE `post_products` (`id` integer PRIMARY KEY AUTOINCREMENT,`post_id` integer NOT NULL,`product_id` integer NOT NULL,`sort` integer NOT NULL DEFAULT 0,`created_at` datetime);
CREATE TABLE `posts` (`id` integer PRIMARY KEY AUTOINCREMENT,`slug` text NOT NULL,`type` text NOT NULL,`title_json` json NOT NULL,`summary_json` json,`content_json` json,`thumbnail` text,`category_id` integer,`is_published` numeric DEFAULT false,`published_at` datetime,`created_at` datetime,`deleted_at` datetime);
CREATE TABLE `procurement_orders` (`notification_pending` numeric NOT NULL DEFAULT false,`cancellation_pending` numeric NOT NULL DEFAULT false,`id` integer PRIMARY KEY AUTOINCREMENT,`connection_id` integer NOT NULL,`local_order_id` integer NOT NULL,`local_order_no` varchar(64),`upstream_order_id` integer,`upstream_order_external_id` varchar(255),`request_snapshot` text,`connection_snapshot` text,`upstream_order_no` varchar(64),`status` varchar(20) NOT NULL DEFAULT "pending",`upstream_amount` decimal(20,2) NOT NULL DEFAULT "0",`upstream_currency` varchar(10) NOT NULL DEFAULT "",`local_sell_amount` decimal(20,2) NOT NULL DEFAULT "0",`currency` varchar(10) NOT NULL,`error_message` text,`retry_count` integer NOT NULL DEFAULT 0,`next_retry_at` datetime,`upstream_payload` text,`trace_id` varchar(64),`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_procurement_orders_connection` FOREIGN KEY (`connection_id`) REFERENCES `site_connections`(`id`),CONSTRAINT `fk_procurement_orders_local_order` FOREIGN KEY (`local_order_id`) REFERENCES `orders`(`id`) ON DELETE NO ACTION ON UPDATE NO ACTION);
CREATE TABLE `product_mappings` (`upstream_product_external_id` varchar(255) NOT NULL DEFAULT "",`id` integer PRIMARY KEY AUTOINCREMENT,`connection_id` integer NOT NULL,`local_product_id` integer NOT NULL,`upstream_product_id` integer NOT NULL,`upstream_fulfillment_type` varchar(20) NOT NULL DEFAULT "manual",`upstream_status` varchar(16) NOT NULL DEFAULT "active",`is_active` numeric NOT NULL DEFAULT true,`last_synced_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_product_mappings_connection` FOREIGN KEY (`connection_id`) REFERENCES `site_connections`(`id`),CONSTRAINT `fk_product_mappings_product` FOREIGN KEY (`local_product_id`) REFERENCES `products`(`id`));
CREATE TABLE `product_skus` (`id` integer PRIMARY KEY AUTOINCREMENT,`product_id` integer NOT NULL,`sku_code` varchar(64) NOT NULL,`spec_values_json` json,`price_amount` decimal(20,2) NOT NULL DEFAULT "0",`cost_price_amount` decimal(20,2) NOT NULL DEFAULT "0",`manual_stock_total` integer NOT NULL DEFAULT 0,`manual_stock_locked` integer NOT NULL DEFAULT 0,`manual_stock_sold` integer NOT NULL DEFAULT 0,`is_active` numeric DEFAULT true,`sort_order` integer DEFAULT 0,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_products_sk_us` FOREIGN KEY (`product_id`) REFERENCES `products`(`id`));
CREATE TABLE `products` (`id` integer PRIMARY KEY AUTOINCREMENT,`category_id` integer NOT NULL,`slug` text NOT NULL,`seo_meta_json` json,`title_json` json NOT NULL,`description_json` json,`content_json` json,`instructions_json` json,`price_amount` decimal(20,2) NOT NULL DEFAULT "0",`cost_price_amount` decimal(20,2) NOT NULL DEFAULT "0",`wholesale_prices` json,`images` json,`tags` json,`purchase_type` varchar(20) NOT NULL DEFAULT "member",`min_purchase_quantity` integer NOT NULL DEFAULT 0,`max_purchase_quantity` integer NOT NULL DEFAULT 0,`stock_display_mode` varchar(20) NOT NULL DEFAULT "exact",`fulfillment_type` varchar(20) NOT NULL DEFAULT "manual",`manual_form_schema_json` json,`manual_stock_total` integer NOT NULL DEFAULT 0,`manual_stock_locked` integer NOT NULL DEFAULT 0,`manual_stock_sold` integer NOT NULL DEFAULT 0,`payment_channel_ids` text,`is_affiliate_enabled` numeric NOT NULL DEFAULT false,`is_mapped` numeric NOT NULL DEFAULT false,`is_active` numeric DEFAULT false,`sort_order` integer DEFAULT 0,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_products_category` FOREIGN KEY (`category_id`) REFERENCES `categories`(`id`));
CREATE TABLE `promotions` (`id` integer PRIMARY KEY AUTOINCREMENT,`name` text NOT NULL,`scope_type` text NOT NULL,`scope_ref_id` integer NOT NULL,`type` text NOT NULL,`value` decimal(20,2) NOT NULL,`min_amount` decimal(20,2) NOT NULL DEFAULT "0",`starts_at` datetime,`ends_at` datetime,`is_active` numeric NOT NULL DEFAULT true,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `reconciliation_items` (`id` integer PRIMARY KEY AUTOINCREMENT,`job_id` integer NOT NULL,`procurement_order_id` integer,`local_order_no` varchar(64),`upstream_order_external_id` varchar(255),`upstream_order_no` varchar(64),`local_status` varchar(20),`upstream_status` varchar(20),`local_amount` decimal(20,2) NOT NULL DEFAULT "0",`upstream_amount` decimal(20,2) NOT NULL DEFAULT "0",`local_currency` varchar(10),`upstream_currency` varchar(10),`diagnostic` varchar(64),`mismatch_type` varchar(40),`resolved` numeric NOT NULL DEFAULT false,`resolved_by` integer,`resolved_at` datetime,`remark` text,`created_at` datetime);
CREATE TABLE `reconciliation_jobs` (`id` integer PRIMARY KEY AUTOINCREMENT,`connection_id` integer NOT NULL,`type` varchar(20) NOT NULL,`status` varchar(20) NOT NULL DEFAULT "pending",`time_range_start` datetime,`time_range_end` datetime,`total_count` integer NOT NULL DEFAULT 0,`matched_count` integer NOT NULL DEFAULT 0,`mismatched_count` integer NOT NULL DEFAULT 0,`result_json` text,`started_at` datetime,`finished_at` datetime,`created_at` datetime,`updated_at` datetime,CONSTRAINT `fk_reconciliation_jobs_connection` FOREIGN KEY (`connection_id`) REFERENCES `site_connections`(`id`));
CREATE TABLE `reseller_balance_accounts` (`id` integer PRIMARY KEY AUTOINCREMENT,`reseller_id` integer NOT NULL,`currency` varchar(16) NOT NULL,`status` varchar(32) NOT NULL DEFAULT "normal",`available_amount_cache` decimal(20,2) NOT NULL DEFAULT "0",`locked_amount_cache` decimal(20,2) NOT NULL DEFAULT "0",`negative_amount_cache` decimal(20,2) NOT NULL DEFAULT "0",`last_ledger_entry_id` integer NOT NULL DEFAULT 0,`risk_note` text,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_reseller_balance_accounts_profile` FOREIGN KEY (`reseller_id`) REFERENCES `reseller_profiles`(`id`));
CREATE TABLE `reseller_domains` (`id` integer PRIMARY KEY AUTOINCREMENT,`reseller_id` integer NOT NULL,`domain` varchar(255) NOT NULL,`type` varchar(24) NOT NULL,`verification_token` varchar(128),`verification_status` varchar(24) NOT NULL DEFAULT "pending",`status` varchar(24) NOT NULL DEFAULT "pending_review",`is_primary` numeric NOT NULL DEFAULT false,`verified_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_reseller_domains_profile` FOREIGN KEY (`reseller_id`) REFERENCES `reseller_profiles`(`id`));
CREATE TABLE `reseller_ledger_entries` (`id` integer PRIMARY KEY AUTOINCREMENT,`reseller_id` integer NOT NULL,`order_id` integer,`type` varchar(32) NOT NULL,`amount` decimal(20,2) NOT NULL DEFAULT "0",`currency` varchar(16) NOT NULL,`idempotency_key` varchar(160) NOT NULL,`metadata_json` json,`status` varchar(32) NOT NULL,`available_at` datetime,`withdraw_request_id` integer,`remark` text,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_reseller_ledger_entries_order` FOREIGN KEY (`order_id`) REFERENCES `orders`(`id`),CONSTRAINT `fk_reseller_ledger_entries_profile` FOREIGN KEY (`reseller_id`) REFERENCES `reseller_profiles`(`id`));
CREATE TABLE `reseller_order_snapshots` (`id` integer PRIMARY KEY AUTOINCREMENT,`order_id` integer NOT NULL,`reseller_id` integer NOT NULL,`domain` varchar(255) NOT NULL,`currency` varchar(16) NOT NULL,`reseller_user_id` integer NOT NULL,`buyer_user_id` integer NOT NULL DEFAULT 0,`base_amount` decimal(20,2) NOT NULL DEFAULT "0",`reseller_amount` decimal(20,2) NOT NULL DEFAULT "0",`profit_amount` decimal(20,2) NOT NULL DEFAULT "0",`profit_eligible` numeric NOT NULL DEFAULT true,`profit_block_reason` varchar(64),`pricing_snapshot_json` json,`risk_snapshot_json` json,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_reseller_order_snapshots_order` FOREIGN KEY (`order_id`) REFERENCES `orders`(`id`));
CREATE TABLE `reseller_product_settings` (`id` integer PRIMARY KEY AUTOINCREMENT,`reseller_id` integer NOT NULL,`product_id` integer NOT NULL,`sku_id` integer NOT NULL DEFAULT 0,`is_listed` numeric NOT NULL DEFAULT true,`pricing_mode` varchar(32) NOT NULL DEFAULT "inherit",`markup_percent` decimal(10,2) NOT NULL DEFAULT "0",`fixed_markup_amount` decimal(20,2) NOT NULL DEFAULT "0",`fixed_price_amount` decimal(20,2) NOT NULL DEFAULT "0",`sort_order` integer NOT NULL DEFAULT 0,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_reseller_product_settings_product` FOREIGN KEY (`product_id`) REFERENCES `products`(`id`),CONSTRAINT `fk_reseller_product_settings_profile` FOREIGN KEY (`reseller_id`) REFERENCES `reseller_profiles`(`id`));
CREATE TABLE `reseller_profiles` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL,`status` varchar(32) NOT NULL DEFAULT "pending_review",`apply_reason` text,`reject_reason` text,`default_markup_percent` decimal(10,2) NOT NULL DEFAULT "0",`max_markup_percent` decimal(10,2) NOT NULL DEFAULT "0",`settlement_status` varchar(32) NOT NULL DEFAULT "normal",`reviewed_by` integer,`reviewed_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_reseller_profiles_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`));
CREATE TABLE `reseller_related_accounts` (`id` integer PRIMARY KEY AUTOINCREMENT,`reseller_id` integer NOT NULL,`user_id` integer NOT NULL,`relation_type` varchar(32) NOT NULL,`source` varchar(64) NOT NULL,`status` varchar(32) NOT NULL DEFAULT "active",`remark` text,`created_by` integer,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `reseller_site_configs` (`id` integer PRIMARY KEY AUTOINCREMENT,`reseller_id` integer NOT NULL,`site_name` varchar(120),`logo` varchar(500),`favicon` varchar(500),`announcement_json` json,`support_json` json,`seo_json` json,`footer_links_json` json,`nav_config_json` json,`theme_json` json,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_reseller_site_configs_profile` FOREIGN KEY (`reseller_id`) REFERENCES `reseller_profiles`(`id`));
CREATE TABLE `reseller_withdraw_requests` (`id` integer PRIMARY KEY AUTOINCREMENT,`reseller_id` integer NOT NULL,`amount` decimal(20,2) NOT NULL DEFAULT "0",`currency` varchar(16) NOT NULL,`channel` varchar(64) NOT NULL,`account` varchar(255) NOT NULL,`status` varchar(32) NOT NULL,`reject_reason` text,`processed_by` integer,`processed_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,CONSTRAINT `fk_reseller_withdraw_requests_processor` FOREIGN KEY (`processed_by`) REFERENCES `admins`(`id`),CONSTRAINT `fk_reseller_withdraw_requests_profile` FOREIGN KEY (`reseller_id`) REFERENCES `reseller_profiles`(`id`));
CREATE TABLE schema_migrations (
			version BIGINT NOT NULL PRIMARY KEY, completed_at VARCHAR(40) NOT NULL
		);
CREATE TABLE `settings` (`key` text,`value_json` json,PRIMARY KEY (`key`));
CREATE TABLE `site_connections` (`id` integer PRIMARY KEY AUTOINCREMENT,`name` varchar(100) NOT NULL,`base_url` varchar(500) NOT NULL,`api_key` varchar(64) NOT NULL,`api_secret` varchar(512) NOT NULL,`http_config_encrypted` text,`http_config_revision` integer NOT NULL DEFAULT 0,`protocol` varchar(20) NOT NULL DEFAULT "dujiao-next",`callback_url` varchar(500),`status` varchar(20) NOT NULL DEFAULT "pending",`last_ping_at` datetime,`last_ping_ok` numeric NOT NULL DEFAULT false,`retry_max` integer NOT NULL DEFAULT 5,`retry_intervals` varchar(200) NOT NULL DEFAULT "[30,60,300]",`exchange_rate` decimal(16,6) NOT NULL DEFAULT "1",`price_markup_percent` decimal(10,4) NOT NULL DEFAULT "0",`price_rounding_mode` varchar(20) NOT NULL DEFAULT "none",`auto_sync_price` numeric NOT NULL DEFAULT false,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `sku_mappings` (`upstream_sku_external_id` varchar(255) NOT NULL DEFAULT "",`product_level` numeric NOT NULL DEFAULT false,`id` integer PRIMARY KEY AUTOINCREMENT,`product_mapping_id` integer NOT NULL,`local_sku_id` integer NOT NULL,`upstream_sku_id` integer NOT NULL,`upstream_price` decimal(20,2) NOT NULL DEFAULT "0",`upstream_stock` integer NOT NULL DEFAULT 0,`upstream_is_active` numeric NOT NULL DEFAULT true,`stock_synced_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `telegram_bot_contacts` (`id` integer PRIMARY KEY AUTOINCREMENT,`bot_id` integer NOT NULL,`telegram_user_id` integer NOT NULL,`chat_id` integer NOT NULL,`user_id` integer,`reachable` numeric NOT NULL,`locale` text,`created_at` datetime,`updated_at` datetime);
CREATE TABLE `telegram_bot_deliveries` (`id` integer PRIMARY KEY AUTOINCREMENT,`bot_id` integer NOT NULL,`key` text NOT NULL,`chat_id` integer,`telegram_user_id` integer,`user_id` integer,`broadcast_id` integer,`payload_cipher` text,`priority` integer,`status` text,`attempts` integer,`available_at` datetime,`claimed_until` datetime,`claim` text,`purpose` text,`operation` text,`panel_epoch` text,`panel_revision` integer NOT NULL DEFAULT 0,`target_message_id` integer NOT NULL DEFAULT 0,`previous_message_id` integer NOT NULL DEFAULT 0,`message_kind` text,`message_id` integer,`last_error` text,`created_at` datetime,`updated_at` datetime);
CREATE TABLE `telegram_bot_inbox` (`id` integer PRIMARY KEY AUTOINCREMENT,`bot_id` integer NOT NULL,`update_id` integer NOT NULL,`payload_cipher` text NOT NULL,`status` text NOT NULL,`attempts` integer,`available_at` datetime,`claimed_until` datetime,`claim` text,`last_error` text,`created_at` datetime,`updated_at` datetime);
CREATE TABLE `telegram_bot_instances` (`id` integer PRIMARY KEY AUTOINCREMENT,`telegram_id` integer NOT NULL,`username` text,`token_cipher` text NOT NULL,`enabled` numeric,`mode` text NOT NULL DEFAULT "internal",`version` integer NOT NULL DEFAULT 1,`applied_version` integer,`generation` integer NOT NULL DEFAULT 0,`offset` integer,`state` text NOT NULL DEFAULT "stopped",`last_error` text,`profile_name` text,`profile_description` text,`profile_cover_url` text,`profile_version` integer NOT NULL DEFAULT 1,`profile_sync_error` text,`last_seen_at` datetime,`last_received_at` datetime,`last_sent_at` datetime,`created_at` datetime,`updated_at` datetime);
CREATE TABLE `telegram_bot_media_cache` (`id` integer PRIMARY KEY AUTOINCREMENT,`bot_id` integer NOT NULL,`path_hash` text NOT NULL,`content_hash` text NOT NULL,`file_id` text NOT NULL,`updated_at` datetime);
CREATE TABLE `telegram_bot_sessions` (`id` integer PRIMARY KEY AUTOINCREMENT,`bot_id` integer NOT NULL,`telegram_user_id` integer NOT NULL,`chat_id` integer NOT NULL,`version` integer NOT NULL,`state` text,`data_cipher` text,`panel_epoch` text,`panel_revision` integer NOT NULL DEFAULT 0,`panel_message_id` integer NOT NULL DEFAULT 0,`panel_message_epoch` text,`panel_kind` text,`panel_state` text,`panel_delivered_revision` integer NOT NULL DEFAULT 0,`expires_at` datetime,`created_at` datetime,`updated_at` datetime);
CREATE TABLE `telegram_broadcasts` (`bot_id` integer,`delivery_version` integer NOT NULL DEFAULT 0,`payload_cipher` text,`unknown_count` integer NOT NULL DEFAULT 0,`id` integer PRIMARY KEY AUTOINCREMENT,`title` text NOT NULL,`recipient_type` text NOT NULL,`filters_json` json,`recipient_chat_ids` json,`recipient_count` integer NOT NULL DEFAULT 0,`success_count` integer NOT NULL DEFAULT 0,`failed_count` integer NOT NULL DEFAULT 0,`status` text NOT NULL DEFAULT "pending",`message_html` text NOT NULL,`attachment_url` text,`attachment_name` text,`started_at` datetime,`completed_at` datetime,`last_error` text,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `telegram_business_events` (`id` integer PRIMARY KEY AUTOINCREMENT,`event_key` text NOT NULL,`kind` text NOT NULL,`order_id` integer,`parent_order_id` integer,`payment_id` integer,`recharge_id` integer,`fulfillment_id` integer,`user_id` integer NOT NULL,`bot_id` integer NOT NULL,`telegram_id` integer NOT NULL,`private_chat_id` integer NOT NULL,`status` text NOT NULL,`attempts` integer,`available_at` datetime NOT NULL,`lease_owner` text,`lease_until` datetime,`delivered_at` datetime,`created_at` datetime,`updated_at` datetime);
CREATE TABLE `telegram_history_grants` (`id` integer PRIMARY KEY AUTOINCREMENT,`bot_id` integer NOT NULL,`telegram_user_id` varchar(255) NOT NULL,`previous_user_id` integer NOT NULL,`current_user_id` integer NOT NULL,`identity_id` integer NOT NULL,`status` varchar(16) NOT NULL,`created_at` datetime,`updated_at` datetime,`revoked_at` datetime);
CREATE TABLE `telegram_order_operations` (`id` integer PRIMARY KEY AUTOINCREMENT,`operation_key` text NOT NULL,`request_hash` text NOT NULL,`user_id` integer NOT NULL,`order_id` integer NOT NULL,`created_at` datetime);
CREATE TABLE `telegram_order_sources` (`id` integer PRIMARY KEY AUTOINCREMENT,`order_id` integer NOT NULL,`user_id` integer NOT NULL,`bot_id` integer NOT NULL,`telegram_id` integer NOT NULL,`private_chat_id` integer NOT NULL,`created_at` datetime);
CREATE TABLE `user_login_logs` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer,`email` text NOT NULL,`status` text NOT NULL,`fail_reason` text,`client_ip` varchar(64),`user_agent` text,`login_source` varchar(32),`request_id` varchar(64),`created_at` datetime);
CREATE TABLE `user_oauth_identities` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL,`provider` varchar(32) NOT NULL,`provider_user_id` varchar(255) NOT NULL,`username` varchar(128),`avatar_url` text,`auth_at` datetime,`created_at` datetime,`updated_at` datetime);
CREATE TABLE `users` (`id` integer PRIMARY KEY AUTOINCREMENT,`email` text NOT NULL,`password_hash` text NOT NULL,`password_setup_required` numeric NOT NULL DEFAULT false,`display_name` text DEFAULT "",`locale` text DEFAULT "zh-CN",`status` text DEFAULT "active",`member_level_id` integer NOT NULL DEFAULT 0,`total_recharged` decimal(20,2) NOT NULL DEFAULT "0",`total_spent` decimal(20,2) NOT NULL DEFAULT "0",`admin_note` text,`token_version` integer NOT NULL DEFAULT 0,`token_invalid_before` datetime,`totp_secret` varchar(512) DEFAULT "",`totp_enabled_at` datetime,`totp_pending_secret` varchar(512) DEFAULT "",`totp_pending_expires_at` datetime,`recovery_codes` text,`email_verified_at` datetime,`last_login_at` datetime,`invite_code_id` integer,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `wallet_accounts` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL,`balance` decimal(20,2) NOT NULL DEFAULT "0",`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `wallet_recharge_orders` (`id` integer PRIMARY KEY AUTOINCREMENT,`recharge_no` varchar(40) NOT NULL,`user_id` integer NOT NULL,`payment_id` integer NOT NULL,`channel_id` integer NOT NULL,`provider_type` varchar(32) NOT NULL,`channel_type` varchar(32) NOT NULL,`interaction_mode` varchar(32) NOT NULL,`amount` decimal(20,2) NOT NULL,`payable_amount` decimal(20,2) NOT NULL,`fee_rate` decimal(6,2) NOT NULL DEFAULT "0",`fee_amount` decimal(20,2) NOT NULL DEFAULT "0",`currency` varchar(16) NOT NULL DEFAULT "CNY",`status` varchar(20) NOT NULL,`remark` varchar(255),`paid_at` datetime,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE TABLE `wallet_transactions` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL,`operator_admin_id` integer,`order_id` integer,`type` varchar(40) NOT NULL,`direction` varchar(16) NOT NULL,`amount` decimal(20,2) NOT NULL,`balance_before` decimal(20,2) NOT NULL DEFAULT "0",`balance_after` decimal(20,2) NOT NULL DEFAULT "0",`currency` varchar(16) NOT NULL DEFAULT "CNY",`reference` varchar(120),`remark` varchar(255),`created_at` datetime,`updated_at` datetime,`deleted_at` datetime);
CREATE INDEX `idx_admin_login_logs_admin_id` ON `admin_login_logs`(`admin_id`);
CREATE INDEX `idx_admin_login_logs_client_ip` ON `admin_login_logs`(`client_ip`);
CREATE INDEX `idx_admin_login_logs_created_at` ON `admin_login_logs`(`created_at`);
CREATE INDEX `idx_admin_login_logs_event_type` ON `admin_login_logs`(`event_type`);
CREATE INDEX `idx_admin_login_logs_fail_reason` ON `admin_login_logs`(`fail_reason`);
CREATE INDEX `idx_admin_login_logs_operator_id` ON `admin_login_logs`(`operator_id`);
CREATE INDEX `idx_admin_login_logs_request_id` ON `admin_login_logs`(`request_id`);
CREATE INDEX `idx_admin_login_logs_status` ON `admin_login_logs`(`status`);
CREATE INDEX `idx_admin_login_logs_username` ON `admin_login_logs`(`username`);
CREATE INDEX `idx_admins_created_at` ON `admins`(`created_at`);
CREATE INDEX `idx_admins_deleted_at` ON `admins`(`deleted_at`);
CREATE INDEX `idx_admins_is_super` ON `admins`(`is_super`);
CREATE INDEX `idx_admins_token_invalid_before` ON `admins`(`token_invalid_before`);
CREATE INDEX `idx_admins_totp_enabled_at` ON `admins`(`totp_enabled_at`);
CREATE UNIQUE INDEX `idx_admins_username` ON `admins`(`username`);
CREATE INDEX `idx_affiliate_clicks_affiliate_profile_id` ON `affiliate_clicks`(`affiliate_profile_id`);
CREATE INDEX `idx_affiliate_clicks_created_at` ON `affiliate_clicks`(`created_at`);
CREATE INDEX `idx_affiliate_clicks_visitor_key` ON `affiliate_clicks`(`visitor_key`);
CREATE UNIQUE INDEX `idx_affiliate_commission_unique` ON `affiliate_commissions`(`affiliate_profile_id`,`order_id`,`commission_type`);
CREATE INDEX `idx_affiliate_commissions_affiliate_profile_id` ON `affiliate_commissions`(`affiliate_profile_id`);
CREATE INDEX `idx_affiliate_commissions_available_at` ON `affiliate_commissions`(`available_at`);
CREATE INDEX `idx_affiliate_commissions_confirm_at` ON `affiliate_commissions`(`confirm_at`);
CREATE INDEX `idx_affiliate_commissions_created_at` ON `affiliate_commissions`(`created_at`);
CREATE INDEX `idx_affiliate_commissions_deleted_at` ON `affiliate_commissions`(`deleted_at`);
CREATE INDEX `idx_affiliate_commissions_order_id` ON `affiliate_commissions`(`order_id`);
CREATE INDEX `idx_affiliate_commissions_order_item_id` ON `affiliate_commissions`(`order_item_id`);
CREATE INDEX `idx_affiliate_commissions_status` ON `affiliate_commissions`(`status`);
CREATE INDEX `idx_affiliate_commissions_updated_at` ON `affiliate_commissions`(`updated_at`);
CREATE INDEX `idx_affiliate_commissions_withdraw_request_id` ON `affiliate_commissions`(`withdraw_request_id`);
CREATE UNIQUE INDEX `idx_affiliate_profiles_affiliate_code` ON `affiliate_profiles`(`affiliate_code`);
CREATE INDEX `idx_affiliate_profiles_created_at` ON `affiliate_profiles`(`created_at`);
CREATE INDEX `idx_affiliate_profiles_deleted_at` ON `affiliate_profiles`(`deleted_at`);
CREATE INDEX `idx_affiliate_profiles_status` ON `affiliate_profiles`(`status`);
CREATE INDEX `idx_affiliate_profiles_updated_at` ON `affiliate_profiles`(`updated_at`);
CREATE UNIQUE INDEX `idx_affiliate_profiles_user_id` ON `affiliate_profiles`(`user_id`);
CREATE INDEX `idx_affiliate_withdraw_requests_affiliate_profile_id` ON `affiliate_withdraw_requests`(`affiliate_profile_id`);
CREATE INDEX `idx_affiliate_withdraw_requests_created_at` ON `affiliate_withdraw_requests`(`created_at`);
CREATE INDEX `idx_affiliate_withdraw_requests_deleted_at` ON `affiliate_withdraw_requests`(`deleted_at`);
CREATE INDEX `idx_affiliate_withdraw_requests_processed_at` ON `affiliate_withdraw_requests`(`processed_at`);
CREATE INDEX `idx_affiliate_withdraw_requests_processed_by` ON `affiliate_withdraw_requests`(`processed_by`);
CREATE INDEX `idx_affiliate_withdraw_requests_status` ON `affiliate_withdraw_requests`(`status`);
CREATE INDEX `idx_affiliate_withdraw_requests_updated_at` ON `affiliate_withdraw_requests`(`updated_at`);
CREATE UNIQUE INDEX `idx_api_credentials_api_key` ON `api_credentials`(`api_key`);
CREATE INDEX `idx_api_credentials_created_at` ON `api_credentials`(`created_at`);
CREATE INDEX `idx_api_credentials_deleted_at` ON `api_credentials`(`deleted_at`);
CREATE INDEX `idx_api_credentials_updated_at` ON `api_credentials`(`updated_at`);
CREATE UNIQUE INDEX `idx_api_credentials_user_id` ON `api_credentials`(`user_id`);
CREATE INDEX `idx_authz_audit_logs_action` ON `authz_audit_logs`(`action`);
CREATE INDEX `idx_authz_audit_logs_created_at` ON `authz_audit_logs`(`created_at`);
CREATE INDEX `idx_authz_audit_logs_method` ON `authz_audit_logs`(`method`);
CREATE INDEX `idx_authz_audit_logs_object` ON `authz_audit_logs`(`object`);
CREATE INDEX `idx_authz_audit_logs_operator_admin_id` ON `authz_audit_logs`(`operator_admin_id`);
CREATE INDEX `idx_authz_audit_logs_operator_username` ON `authz_audit_logs`(`operator_username`);
CREATE INDEX `idx_authz_audit_logs_request_id` ON `authz_audit_logs`(`request_id`);
CREATE INDEX `idx_authz_audit_logs_role` ON `authz_audit_logs`(`role`);
CREATE INDEX `idx_authz_audit_logs_target_admin_id` ON `authz_audit_logs`(`target_admin_id`);
CREATE INDEX `idx_authz_audit_logs_target_username` ON `authz_audit_logs`(`target_username`);
CREATE INDEX `idx_banners_created_at` ON `banners`(`created_at`);
CREATE INDEX `idx_banners_deleted_at` ON `banners`(`deleted_at`);
CREATE INDEX `idx_banners_end_at` ON `banners`(`end_at`);
CREATE INDEX `idx_banners_is_active` ON `banners`(`is_active`);
CREATE INDEX `idx_banners_name` ON `banners`(`name`);
CREATE INDEX `idx_banners_position` ON `banners`(`position`);
CREATE INDEX `idx_banners_sort_order` ON `banners`(`sort_order`);
CREATE INDEX `idx_banners_start_at` ON `banners`(`start_at`);
CREATE UNIQUE INDEX `idx_bot_contact` ON `telegram_bot_contacts`(`bot_id`,`telegram_user_id`);
CREATE UNIQUE INDEX `idx_bot_delivery_key` ON `telegram_bot_deliveries`(`bot_id`,`key`);
CREATE UNIQUE INDEX `idx_bot_media` ON `telegram_bot_media_cache`(`bot_id`,`path_hash`,`content_hash`);
CREATE UNIQUE INDEX `idx_bot_session` ON `telegram_bot_sessions`(`bot_id`,`telegram_user_id`,`chat_id`);
CREATE UNIQUE INDEX `idx_bot_update` ON `telegram_bot_inbox`(`bot_id`,`update_id`);
CREATE UNIQUE INDEX `idx_card_secret_batches_batch_no` ON `card_secret_batches`(`batch_no`);
CREATE INDEX `idx_card_secret_batches_created_at` ON `card_secret_batches`(`created_at`);
CREATE INDEX `idx_card_secret_batches_created_by` ON `card_secret_batches`(`created_by`);
CREATE INDEX `idx_card_secret_batches_deleted_at` ON `card_secret_batches`(`deleted_at`);
CREATE INDEX `idx_card_secret_batches_product_id` ON `card_secret_batches`(`product_id`);
CREATE INDEX `idx_card_secret_batches_sk_uid` ON `card_secret_batches`(`sku_id`);
CREATE INDEX `idx_card_secret_batches_updated_at` ON `card_secret_batches`(`updated_at`);
CREATE INDEX `idx_card_secret_reserve` ON `card_secrets`(`product_id`,`sku_id`,`status`);
CREATE INDEX `idx_card_secrets_batch_id` ON `card_secrets`(`batch_id`);
CREATE INDEX `idx_card_secrets_created_at` ON `card_secrets`(`created_at`);
CREATE INDEX `idx_card_secrets_deleted_at` ON `card_secrets`(`deleted_at`);
CREATE INDEX `idx_card_secrets_order_id` ON `card_secrets`(`order_id`);
CREATE INDEX `idx_card_secrets_reserved_at` ON `card_secrets`(`reserved_at`);
CREATE INDEX `idx_card_secrets_updated_at` ON `card_secrets`(`updated_at`);
CREATE INDEX `idx_card_secrets_used_at` ON `card_secrets`(`used_at`);
CREATE INDEX `idx_cart_items_created_at` ON `cart_items`(`created_at`);
CREATE INDEX `idx_cart_items_deleted_at` ON `cart_items`(`deleted_at`);
CREATE INDEX `idx_cart_items_updated_at` ON `cart_items`(`updated_at`);
CREATE UNIQUE INDEX `idx_cart_user_product_sku` ON `cart_items`(`user_id`,`product_id`,`sku_id`);
CREATE INDEX `idx_categories_created_at` ON `categories`(`created_at`);
CREATE INDEX `idx_categories_deleted_at` ON `categories`(`deleted_at`);
CREATE INDEX `idx_categories_is_active` ON `categories`(`is_active`);
CREATE INDEX `idx_categories_parent_id` ON `categories`(`parent_id`);
CREATE UNIQUE INDEX `idx_categories_slug` ON `categories`(`slug`);
CREATE INDEX `idx_categories_sort_order` ON `categories`(`sort_order`);
CREATE UNIQUE INDEX `idx_channel_clients_channel_key` ON `channel_clients`(`channel_key`);
CREATE INDEX `idx_channel_clients_channel_type` ON `channel_clients`(`channel_type`);
CREATE INDEX `idx_channel_clients_deleted_at` ON `channel_clients`(`deleted_at`);
CREATE INDEX `idx_channel_clients_status` ON `channel_clients`(`status`);
CREATE INDEX `idx_coupon_usages_coupon_id` ON `coupon_usages`(`coupon_id`);
CREATE INDEX `idx_coupon_usages_created_at` ON `coupon_usages`(`created_at`);
CREATE INDEX `idx_coupon_usages_deleted_at` ON `coupon_usages`(`deleted_at`);
CREATE INDEX `idx_coupon_usages_order_id` ON `coupon_usages`(`order_id`);
CREATE INDEX `idx_coupon_usages_user_id` ON `coupon_usages`(`user_id`);
CREATE UNIQUE INDEX `idx_coupons_code` ON `coupons`(`code`);
CREATE INDEX `idx_coupons_created_at` ON `coupons`(`created_at`);
CREATE INDEX `idx_coupons_deleted_at` ON `coupons`(`deleted_at`);
CREATE INDEX `idx_coupons_ends_at` ON `coupons`(`ends_at`);
CREATE INDEX `idx_coupons_starts_at` ON `coupons`(`starts_at`);
CREATE INDEX `idx_coupons_updated_at` ON `coupons`(`updated_at`);
CREATE INDEX `idx_downstream_order_refs_api_credential_id` ON `downstream_order_refs`(`api_credential_id`);
CREATE INDEX `idx_downstream_order_refs_created_at` ON `downstream_order_refs`(`created_at`);
CREATE INDEX `idx_downstream_order_refs_downstream_order_no` ON `downstream_order_refs`(`downstream_order_no`);
CREATE UNIQUE INDEX `idx_downstream_order_refs_order_id` ON `downstream_order_refs`(`order_id`);
CREATE INDEX `idx_downstream_order_refs_trace_id` ON `downstream_order_refs`(`trace_id`);
CREATE INDEX `idx_downstream_order_refs_updated_at` ON `downstream_order_refs`(`updated_at`);
CREATE INDEX `idx_email_verify_codes_created_at` ON `email_verify_codes`(`created_at`);
CREATE INDEX `idx_email_verify_codes_deleted_at` ON `email_verify_codes`(`deleted_at`);
CREATE INDEX `idx_email_verify_codes_email` ON `email_verify_codes`(`email`);
CREATE INDEX `idx_email_verify_codes_expires_at` ON `email_verify_codes`(`expires_at`);
CREATE INDEX `idx_email_verify_codes_purpose` ON `email_verify_codes`(`purpose`);
CREATE INDEX `idx_email_verify_codes_sent_at` ON `email_verify_codes`(`sent_at`);
CREATE INDEX `idx_email_verify_codes_user_id` ON `email_verify_codes`(`user_id`);
CREATE INDEX `idx_email_verify_codes_verified_at` ON `email_verify_codes`(`verified_at`);
CREATE INDEX `idx_fulfillments_created_at` ON `fulfillments`(`created_at`);
CREATE INDEX `idx_fulfillments_deleted_at` ON `fulfillments`(`deleted_at`);
CREATE INDEX `idx_fulfillments_delivered_at` ON `fulfillments`(`delivered_at`);
CREATE INDEX `idx_fulfillments_delivered_by` ON `fulfillments`(`delivered_by`);
CREATE UNIQUE INDEX `idx_fulfillments_order_id` ON `fulfillments`(`order_id`);
CREATE INDEX `idx_fulfillments_updated_at` ON `fulfillments`(`updated_at`);
CREATE UNIQUE INDEX `idx_gift_card_batches_batch_no` ON `gift_card_batches`(`batch_no`);
CREATE INDEX `idx_gift_card_batches_created_at` ON `gift_card_batches`(`created_at`);
CREATE INDEX `idx_gift_card_batches_created_by` ON `gift_card_batches`(`created_by`);
CREATE INDEX `idx_gift_card_batches_deleted_at` ON `gift_card_batches`(`deleted_at`);
CREATE INDEX `idx_gift_card_batches_expires_at` ON `gift_card_batches`(`expires_at`);
CREATE INDEX `idx_gift_card_batches_updated_at` ON `gift_card_batches`(`updated_at`);
CREATE INDEX `idx_gift_cards_batch_id` ON `gift_cards`(`batch_id`);
CREATE UNIQUE INDEX `idx_gift_cards_code` ON `gift_cards`(`code`);
CREATE INDEX `idx_gift_cards_created_at` ON `gift_cards`(`created_at`);
CREATE INDEX `idx_gift_cards_deleted_at` ON `gift_cards`(`deleted_at`);
CREATE INDEX `idx_gift_cards_expires_at` ON `gift_cards`(`expires_at`);
CREATE INDEX `idx_gift_cards_redeemed_at` ON `gift_cards`(`redeemed_at`);
CREATE INDEX `idx_gift_cards_redeemed_user_id` ON `gift_cards`(`redeemed_user_id`);
CREATE INDEX `idx_gift_cards_status` ON `gift_cards`(`status`);
CREATE INDEX `idx_gift_cards_updated_at` ON `gift_cards`(`updated_at`);
CREATE INDEX `idx_gift_cards_wallet_txn_id` ON `gift_cards`(`wallet_txn_id`);
CREATE UNIQUE INDEX `idx_invite_codes_code` ON `invite_codes`(`code`);
CREATE INDEX `idx_invite_codes_created_at` ON `invite_codes`(`created_at`);
CREATE INDEX `idx_invite_codes_created_by` ON `invite_codes`(`created_by`);
CREATE INDEX `idx_invite_codes_expires_at` ON `invite_codes`(`expires_at`);
CREATE INDEX `idx_invite_codes_status` ON `invite_codes`(`status`);
CREATE INDEX `idx_media_created_at` ON `media`(`created_at`);
CREATE INDEX `idx_media_deleted_at` ON `media`(`deleted_at`);
CREATE INDEX `idx_media_name` ON `media`(`name`);
CREATE UNIQUE INDEX `idx_media_path` ON `media`(`path`);
CREATE INDEX `idx_media_scene` ON `media`(`scene`);
CREATE UNIQUE INDEX `idx_member_level_price` ON `member_level_prices`(`member_level_id`,`product_id`,`sku_id`);
CREATE INDEX `idx_member_level_prices_created_at` ON `member_level_prices`(`created_at`);
CREATE INDEX `idx_member_level_prices_deleted_at` ON `member_level_prices`(`deleted_at`);
CREATE INDEX `idx_member_level_prices_updated_at` ON `member_level_prices`(`updated_at`);
CREATE INDEX `idx_member_levels_created_at` ON `member_levels`(`created_at`);
CREATE INDEX `idx_member_levels_deleted_at` ON `member_levels`(`deleted_at`);
CREATE UNIQUE INDEX `idx_member_levels_slug` ON `member_levels`(`slug`);
CREATE INDEX `idx_member_levels_updated_at` ON `member_levels`(`updated_at`);
CREATE INDEX `idx_notification_logs_biz_id` ON `notification_logs`(`biz_id`);
CREATE INDEX `idx_notification_logs_biz_type` ON `notification_logs`(`biz_type`);
CREATE INDEX `idx_notification_logs_channel` ON `notification_logs`(`channel`);
CREATE INDEX `idx_notification_logs_created_at` ON `notification_logs`(`created_at`);
CREATE INDEX `idx_notification_logs_event_type` ON `notification_logs`(`event_type`);
CREATE INDEX `idx_notification_logs_is_test` ON `notification_logs`(`is_test`);
CREATE INDEX `idx_notification_logs_locale` ON `notification_logs`(`locale`);
CREATE INDEX `idx_notification_logs_recipient` ON `notification_logs`(`recipient`);
CREATE INDEX `idx_notification_logs_status` ON `notification_logs`(`status`);
CREATE INDEX `idx_order_items_created_at` ON `order_items`(`created_at`);
CREATE INDEX `idx_order_items_deleted_at` ON `order_items`(`deleted_at`);
CREATE INDEX `idx_order_items_order_id` ON `order_items`(`order_id`);
CREATE INDEX `idx_order_items_product_id` ON `order_items`(`product_id`);
CREATE INDEX `idx_order_items_promotion_id` ON `order_items`(`promotion_id`);
CREATE INDEX `idx_order_items_sk_uid` ON `order_items`(`sku_id`);
CREATE INDEX `idx_order_items_updated_at` ON `order_items`(`updated_at`);
CREATE INDEX `idx_order_refund_records_created_at` ON `order_refund_records`(`created_at`);
CREATE INDEX `idx_order_refund_records_deleted_at` ON `order_refund_records`(`deleted_at`);
CREATE INDEX `idx_order_refund_records_guest_email` ON `order_refund_records`(`guest_email`);
CREATE INDEX `idx_order_refund_records_order_id` ON `order_refund_records`(`order_id`);
CREATE INDEX `idx_order_refund_records_type` ON `order_refund_records`(`type`);
CREATE INDEX `idx_order_refund_records_updated_at` ON `order_refund_records`(`updated_at`);
CREATE INDEX `idx_order_refund_records_user_id` ON `order_refund_records`(`user_id`);
CREATE INDEX `idx_orders_affiliate_code` ON `orders`(`affiliate_code`);
CREATE INDEX `idx_orders_affiliate_profile_id` ON `orders`(`affiliate_profile_id`);
CREATE INDEX `idx_orders_canceled_at` ON `orders`(`canceled_at`);
CREATE INDEX `idx_orders_coupon_id` ON `orders`(`coupon_id`);
CREATE INDEX `idx_orders_created_at` ON `orders`(`created_at`);
CREATE INDEX `idx_orders_deleted_at` ON `orders`(`deleted_at`);
CREATE INDEX `idx_orders_expires_at` ON `orders`(`expires_at`);
CREATE INDEX `idx_orders_guest_email` ON `orders`(`guest_email`);
CREATE INDEX `idx_orders_member_level_id` ON `orders`(`member_level_id`);
CREATE UNIQUE INDEX `idx_orders_order_no` ON `orders`(`order_no`);
CREATE INDEX `idx_orders_paid_at` ON `orders`(`paid_at`);
CREATE INDEX `idx_orders_parent_id` ON `orders`(`parent_id`);
CREATE INDEX `idx_orders_promotion_id` ON `orders`(`promotion_id`);
CREATE INDEX `idx_orders_reseller_domain` ON `orders`(`reseller_domain`);
CREATE INDEX `idx_orders_reseller_id` ON `orders`(`reseller_id`);
CREATE INDEX `idx_orders_risk_ip` ON `orders`(`risk_ip`);
CREATE INDEX `idx_orders_risk_pending` ON `orders`(`risk_ip`,`user_id`,`status`,`parent_id`,`deleted_at`);
CREATE INDEX `idx_orders_status` ON `orders`(`status`);
CREATE INDEX `idx_orders_updated_at` ON `orders`(`updated_at`);
CREATE INDEX `idx_orders_user_id` ON `orders`(`user_id`);
CREATE INDEX `idx_payment_channels_created_at` ON `payment_channels`(`created_at`);
CREATE INDEX `idx_payment_channels_deleted_at` ON `payment_channels`(`deleted_at`);
CREATE INDEX `idx_payment_channels_is_active` ON `payment_channels`(`is_active`);
CREATE INDEX `idx_payment_channels_updated_at` ON `payment_channels`(`updated_at`);
CREATE UNIQUE INDEX `idx_payment_order_operation` ON `payments`(`operation_key`,`order_id`);
CREATE INDEX `idx_payments_callback_at` ON `payments`(`callback_at`);
CREATE INDEX `idx_payments_channel_id` ON `payments`(`channel_id`);
CREATE INDEX `idx_payments_created_at` ON `payments`(`created_at`);
CREATE INDEX `idx_payments_creation_state` ON `payments`(`creation_state`);
CREATE INDEX `idx_payments_deleted_at` ON `payments`(`deleted_at`);
CREATE INDEX `idx_payments_exception_code` ON `payments`(`exception_code`);
CREATE INDEX `idx_payments_expired_at` ON `payments`(`expired_at`);
CREATE INDEX `idx_payments_fee_policy` ON `payments`(`fee_policy`);
CREATE INDEX `idx_payments_gateway_order_no` ON `payments`(`gateway_order_no`);
CREATE INDEX `idx_payments_order_id` ON `payments`(`order_id`);
CREATE INDEX `idx_payments_paid_at` ON `payments`(`paid_at`);
CREATE INDEX `idx_payments_provider_ref` ON `payments`(`provider_ref`);
CREATE INDEX `idx_payments_status` ON `payments`(`status`);
CREATE INDEX `idx_payments_superseded_at` ON `payments`(`superseded_at`);
CREATE INDEX `idx_payments_superseded_by_payment_id` ON `payments`(`superseded_by_payment_id`);
CREATE INDEX `idx_payments_updated_at` ON `payments`(`updated_at`);
CREATE INDEX `idx_post_categories_deleted_at` ON `post_categories`(`deleted_at`);
CREATE INDEX `idx_post_categories_is_active` ON `post_categories`(`is_active`);
CREATE INDEX `idx_post_categories_parent_id` ON `post_categories`(`parent_id`);
CREATE UNIQUE INDEX `idx_post_categories_slug` ON `post_categories`(`slug`);
CREATE INDEX `idx_post_categories_sort_order` ON `post_categories`(`sort_order`);
CREATE UNIQUE INDEX `idx_post_product` ON `post_products`(`post_id`,`product_id`);
CREATE INDEX `idx_post_products_product_id` ON `post_products`(`product_id`);
CREATE INDEX `idx_posts_category_id` ON `posts`(`category_id`);
CREATE INDEX `idx_posts_created_at` ON `posts`(`created_at`);
CREATE INDEX `idx_posts_deleted_at` ON `posts`(`deleted_at`);
CREATE INDEX `idx_posts_is_published` ON `posts`(`is_published`);
CREATE INDEX `idx_posts_published_at` ON `posts`(`published_at`);
CREATE UNIQUE INDEX `idx_posts_slug` ON `posts`(`slug`);
CREATE INDEX `idx_posts_type` ON `posts`(`type`);
CREATE INDEX `idx_procurement_orders_connection_id` ON `procurement_orders`(`connection_id`);
CREATE INDEX `idx_procurement_orders_created_at` ON `procurement_orders`(`created_at`);
CREATE INDEX `idx_procurement_orders_deleted_at` ON `procurement_orders`(`deleted_at`);
CREATE INDEX `idx_procurement_orders_local_order_no` ON `procurement_orders`(`local_order_no`);
CREATE INDEX `idx_procurement_orders_next_retry_at` ON `procurement_orders`(`next_retry_at`);
CREATE INDEX `idx_procurement_orders_notification_pending` ON `procurement_orders`(`notification_pending`);
CREATE INDEX `idx_procurement_orders_status` ON `procurement_orders`(`status`);
CREATE INDEX `idx_procurement_orders_trace_id` ON `procurement_orders`(`trace_id`);
CREATE INDEX `idx_procurement_orders_updated_at` ON `procurement_orders`(`updated_at`);
CREATE INDEX `idx_procurement_orders_upstream_order_external_id` ON `procurement_orders`(`upstream_order_external_id`);
CREATE INDEX `idx_procurement_orders_upstream_order_no` ON `procurement_orders`(`upstream_order_no`);
CREATE INDEX `idx_product_mappings_connection_id` ON `product_mappings`(`connection_id`);
CREATE INDEX `idx_product_mappings_created_at` ON `product_mappings`(`created_at`);
CREATE INDEX `idx_product_mappings_deleted_at` ON `product_mappings`(`deleted_at`);
CREATE UNIQUE INDEX `idx_product_mappings_local_product_id` ON `product_mappings`(`local_product_id`);
CREATE INDEX `idx_product_mappings_updated_at` ON `product_mappings`(`updated_at`);
CREATE INDEX `idx_product_mappings_upstream_product_external_id` ON `product_mappings`(`upstream_product_external_id`);
CREATE INDEX `idx_product_mappings_upstream_status` ON `product_mappings`(`upstream_status`);
CREATE UNIQUE INDEX `idx_product_sku_code` ON `product_skus`(`product_id`,`sku_code`);
CREATE INDEX `idx_product_skus_created_at` ON `product_skus`(`created_at`);
CREATE INDEX `idx_product_skus_deleted_at` ON `product_skus`(`deleted_at`);
CREATE INDEX `idx_product_skus_is_active` ON `product_skus`(`is_active`);
CREATE INDEX `idx_product_skus_product_id` ON `product_skus`(`product_id`);
CREATE INDEX `idx_product_skus_sort_order` ON `product_skus`(`sort_order`);
CREATE INDEX `idx_product_skus_updated_at` ON `product_skus`(`updated_at`);
CREATE INDEX `idx_products_category_id` ON `products`(`category_id`);
CREATE INDEX `idx_products_created_at` ON `products`(`created_at`);
CREATE INDEX `idx_products_deleted_at` ON `products`(`deleted_at`);
CREATE INDEX `idx_products_is_active` ON `products`(`is_active`);
CREATE INDEX `idx_products_is_affiliate_enabled` ON `products`(`is_affiliate_enabled`);
CREATE INDEX `idx_products_is_mapped` ON `products`(`is_mapped`);
CREATE UNIQUE INDEX `idx_products_slug` ON `products`(`slug`);
CREATE INDEX `idx_products_sort_order` ON `products`(`sort_order`);
CREATE INDEX `idx_promotions_created_at` ON `promotions`(`created_at`);
CREATE INDEX `idx_promotions_deleted_at` ON `promotions`(`deleted_at`);
CREATE INDEX `idx_promotions_ends_at` ON `promotions`(`ends_at`);
CREATE INDEX `idx_promotions_scope_ref_id` ON `promotions`(`scope_ref_id`);
CREATE INDEX `idx_promotions_starts_at` ON `promotions`(`starts_at`);
CREATE INDEX `idx_promotions_updated_at` ON `promotions`(`updated_at`);
CREATE UNIQUE INDEX `idx_provider_user` ON `user_oauth_identities`(`provider`,`provider_user_id`);
CREATE INDEX `idx_reconciliation_items_created_at` ON `reconciliation_items`(`created_at`);
CREATE INDEX `idx_reconciliation_items_job_id` ON `reconciliation_items`(`job_id`);
CREATE INDEX `idx_reconciliation_items_procurement_order_id` ON `reconciliation_items`(`procurement_order_id`);
CREATE INDEX `idx_reconciliation_jobs_connection_id` ON `reconciliation_jobs`(`connection_id`);
CREATE INDEX `idx_reconciliation_jobs_created_at` ON `reconciliation_jobs`(`created_at`);
CREATE INDEX `idx_reconciliation_jobs_updated_at` ON `reconciliation_jobs`(`updated_at`);
CREATE UNIQUE INDEX `idx_reseller_balance_accounts_active_currency` ON `reseller_balance_accounts` (`reseller_id`, `currency`) WHERE deleted_at IS NULL;
CREATE INDEX `idx_reseller_balance_accounts_created_at` ON `reseller_balance_accounts`(`created_at`);
CREATE INDEX `idx_reseller_balance_accounts_currency` ON `reseller_balance_accounts`(`currency`);
CREATE INDEX `idx_reseller_balance_accounts_deleted_at` ON `reseller_balance_accounts`(`deleted_at`);
CREATE INDEX `idx_reseller_balance_accounts_reseller_id` ON `reseller_balance_accounts`(`reseller_id`);
CREATE INDEX `idx_reseller_balance_accounts_status` ON `reseller_balance_accounts`(`status`);
CREATE INDEX `idx_reseller_balance_accounts_updated_at` ON `reseller_balance_accounts`(`updated_at`);
CREATE UNIQUE INDEX `idx_reseller_domains_active_domain` ON `reseller_domains` (`domain`) WHERE deleted_at IS NULL;
CREATE INDEX `idx_reseller_domains_created_at` ON `reseller_domains`(`created_at`);
CREATE INDEX `idx_reseller_domains_deleted_at` ON `reseller_domains`(`deleted_at`);
CREATE INDEX `idx_reseller_domains_domain` ON `reseller_domains`(`domain`);
CREATE INDEX `idx_reseller_domains_reseller_id` ON `reseller_domains`(`reseller_id`);
CREATE INDEX `idx_reseller_domains_status` ON `reseller_domains`(`status`);
CREATE INDEX `idx_reseller_domains_updated_at` ON `reseller_domains`(`updated_at`);
CREATE INDEX `idx_reseller_domains_verification_status` ON `reseller_domains`(`verification_status`);
CREATE INDEX `idx_reseller_domains_verified_at` ON `reseller_domains`(`verified_at`);
CREATE INDEX `idx_reseller_ledger_entries_available_at` ON `reseller_ledger_entries`(`available_at`);
CREATE INDEX `idx_reseller_ledger_entries_created_at` ON `reseller_ledger_entries`(`created_at`);
CREATE INDEX `idx_reseller_ledger_entries_currency` ON `reseller_ledger_entries`(`currency`);
CREATE INDEX `idx_reseller_ledger_entries_deleted_at` ON `reseller_ledger_entries`(`deleted_at`);
CREATE UNIQUE INDEX `idx_reseller_ledger_entries_idempotency_key` ON `reseller_ledger_entries`(`idempotency_key`);
CREATE INDEX `idx_reseller_ledger_entries_order_id` ON `reseller_ledger_entries`(`order_id`);
CREATE INDEX `idx_reseller_ledger_entries_reseller_id` ON `reseller_ledger_entries`(`reseller_id`);
CREATE INDEX `idx_reseller_ledger_entries_status` ON `reseller_ledger_entries`(`status`);
CREATE INDEX `idx_reseller_ledger_entries_type` ON `reseller_ledger_entries`(`type`);
CREATE INDEX `idx_reseller_ledger_entries_updated_at` ON `reseller_ledger_entries`(`updated_at`);
CREATE INDEX `idx_reseller_ledger_entries_withdraw_request_id` ON `reseller_ledger_entries`(`withdraw_request_id`);
CREATE INDEX `idx_reseller_order_snapshots_buyer_user_id` ON `reseller_order_snapshots`(`buyer_user_id`);
CREATE INDEX `idx_reseller_order_snapshots_created_at` ON `reseller_order_snapshots`(`created_at`);
CREATE INDEX `idx_reseller_order_snapshots_deleted_at` ON `reseller_order_snapshots`(`deleted_at`);
CREATE INDEX `idx_reseller_order_snapshots_domain` ON `reseller_order_snapshots`(`domain`);
CREATE UNIQUE INDEX `idx_reseller_order_snapshots_order_id` ON `reseller_order_snapshots`(`order_id`);
CREATE INDEX `idx_reseller_order_snapshots_profit_block_reason` ON `reseller_order_snapshots`(`profit_block_reason`);
CREATE INDEX `idx_reseller_order_snapshots_profit_eligible` ON `reseller_order_snapshots`(`profit_eligible`);
CREATE INDEX `idx_reseller_order_snapshots_reseller_id` ON `reseller_order_snapshots`(`reseller_id`);
CREATE INDEX `idx_reseller_order_snapshots_reseller_user_id` ON `reseller_order_snapshots`(`reseller_user_id`);
CREATE INDEX `idx_reseller_order_snapshots_updated_at` ON `reseller_order_snapshots`(`updated_at`);
CREATE UNIQUE INDEX `idx_reseller_product_settings_active_scope` ON `reseller_product_settings` (`reseller_id`, `product_id`, `sku_id`) WHERE deleted_at IS NULL;
CREATE INDEX `idx_reseller_product_settings_created_at` ON `reseller_product_settings`(`created_at`);
CREATE INDEX `idx_reseller_product_settings_deleted_at` ON `reseller_product_settings`(`deleted_at`);
CREATE INDEX `idx_reseller_product_settings_product_id` ON `reseller_product_settings`(`product_id`);
CREATE INDEX `idx_reseller_product_settings_reseller_id` ON `reseller_product_settings`(`reseller_id`);
CREATE INDEX `idx_reseller_product_settings_sk_uid` ON `reseller_product_settings`(`sku_id`);
CREATE INDEX `idx_reseller_product_settings_sort_order` ON `reseller_product_settings`(`sort_order`);
CREATE INDEX `idx_reseller_product_settings_updated_at` ON `reseller_product_settings`(`updated_at`);
CREATE INDEX `idx_reseller_profiles_created_at` ON `reseller_profiles`(`created_at`);
CREATE INDEX `idx_reseller_profiles_deleted_at` ON `reseller_profiles`(`deleted_at`);
CREATE INDEX `idx_reseller_profiles_reviewed_at` ON `reseller_profiles`(`reviewed_at`);
CREATE INDEX `idx_reseller_profiles_reviewed_by` ON `reseller_profiles`(`reviewed_by`);
CREATE INDEX `idx_reseller_profiles_settlement_status` ON `reseller_profiles`(`settlement_status`);
CREATE INDEX `idx_reseller_profiles_status` ON `reseller_profiles`(`status`);
CREATE INDEX `idx_reseller_profiles_updated_at` ON `reseller_profiles`(`updated_at`);
CREATE UNIQUE INDEX `idx_reseller_profiles_user_id` ON `reseller_profiles`(`user_id`);
CREATE UNIQUE INDEX `idx_reseller_related_accounts_active_user` ON `reseller_related_accounts` (`reseller_id`, `user_id`) WHERE deleted_at IS NULL;
CREATE INDEX `idx_reseller_related_accounts_created_at` ON `reseller_related_accounts`(`created_at`);
CREATE INDEX `idx_reseller_related_accounts_created_by` ON `reseller_related_accounts`(`created_by`);
CREATE INDEX `idx_reseller_related_accounts_deleted_at` ON `reseller_related_accounts`(`deleted_at`);
CREATE INDEX `idx_reseller_related_accounts_relation_type` ON `reseller_related_accounts`(`relation_type`);
CREATE INDEX `idx_reseller_related_accounts_reseller_id` ON `reseller_related_accounts`(`reseller_id`);
CREATE INDEX `idx_reseller_related_accounts_status` ON `reseller_related_accounts`(`status`);
CREATE INDEX `idx_reseller_related_accounts_updated_at` ON `reseller_related_accounts`(`updated_at`);
CREATE INDEX `idx_reseller_related_accounts_user_id` ON `reseller_related_accounts`(`user_id`);
CREATE UNIQUE INDEX `idx_reseller_site_configs_active_reseller` ON `reseller_site_configs` (`reseller_id`) WHERE deleted_at IS NULL;
CREATE INDEX `idx_reseller_site_configs_created_at` ON `reseller_site_configs`(`created_at`);
CREATE INDEX `idx_reseller_site_configs_deleted_at` ON `reseller_site_configs`(`deleted_at`);
CREATE INDEX `idx_reseller_site_configs_reseller_id` ON `reseller_site_configs`(`reseller_id`);
CREATE INDEX `idx_reseller_site_configs_updated_at` ON `reseller_site_configs`(`updated_at`);
CREATE INDEX `idx_reseller_withdraw_requests_created_at` ON `reseller_withdraw_requests`(`created_at`);
CREATE INDEX `idx_reseller_withdraw_requests_currency` ON `reseller_withdraw_requests`(`currency`);
CREATE INDEX `idx_reseller_withdraw_requests_deleted_at` ON `reseller_withdraw_requests`(`deleted_at`);
CREATE INDEX `idx_reseller_withdraw_requests_processed_at` ON `reseller_withdraw_requests`(`processed_at`);
CREATE INDEX `idx_reseller_withdraw_requests_processed_by` ON `reseller_withdraw_requests`(`processed_by`);
CREATE INDEX `idx_reseller_withdraw_requests_reseller_id` ON `reseller_withdraw_requests`(`reseller_id`);
CREATE INDEX `idx_reseller_withdraw_requests_status` ON `reseller_withdraw_requests`(`status`);
CREATE INDEX `idx_reseller_withdraw_requests_updated_at` ON `reseller_withdraw_requests`(`updated_at`);
CREATE INDEX `idx_site_connections_created_at` ON `site_connections`(`created_at`);
CREATE INDEX `idx_site_connections_deleted_at` ON `site_connections`(`deleted_at`);
CREATE INDEX `idx_site_connections_updated_at` ON `site_connections`(`updated_at`);
CREATE INDEX `idx_sku_mappings_created_at` ON `sku_mappings`(`created_at`);
CREATE INDEX `idx_sku_mappings_deleted_at` ON `sku_mappings`(`deleted_at`);
CREATE INDEX `idx_sku_mappings_local_sk_uid` ON `sku_mappings`(`local_sku_id`);
CREATE INDEX `idx_sku_mappings_product_mapping_id` ON `sku_mappings`(`product_mapping_id`);
CREATE INDEX `idx_sku_mappings_updated_at` ON `sku_mappings`(`updated_at`);
CREATE INDEX `idx_telegram_bot_contacts_user_id` ON `telegram_bot_contacts`(`user_id`);
CREATE INDEX `idx_telegram_bot_deliveries_available_at` ON `telegram_bot_deliveries`(`available_at`);
CREATE INDEX `idx_telegram_bot_deliveries_bot_id` ON `telegram_bot_deliveries`(`bot_id`);
CREATE INDEX `idx_telegram_bot_deliveries_broadcast_id` ON `telegram_bot_deliveries`(`broadcast_id`);
CREATE INDEX `idx_telegram_bot_deliveries_chat_id` ON `telegram_bot_deliveries`(`chat_id`);
CREATE INDEX `idx_telegram_bot_deliveries_panel_epoch` ON `telegram_bot_deliveries`(`panel_epoch`);
CREATE INDEX `idx_telegram_bot_deliveries_priority` ON `telegram_bot_deliveries`(`priority`);
CREATE INDEX `idx_telegram_bot_deliveries_purpose` ON `telegram_bot_deliveries`(`purpose`);
CREATE INDEX `idx_telegram_bot_deliveries_status` ON `telegram_bot_deliveries`(`status`);
CREATE INDEX `idx_telegram_bot_inbox_available_at` ON `telegram_bot_inbox`(`available_at`);
CREATE INDEX `idx_telegram_bot_inbox_bot_id` ON `telegram_bot_inbox`(`bot_id`);
CREATE INDEX `idx_telegram_bot_inbox_status` ON `telegram_bot_inbox`(`status`);
CREATE UNIQUE INDEX `idx_telegram_bot_instances_telegram_id` ON `telegram_bot_instances`(`telegram_id`);
CREATE INDEX `idx_telegram_broadcasts_bot_id` ON `telegram_broadcasts`(`bot_id`);
CREATE INDEX `idx_telegram_broadcasts_created_at` ON `telegram_broadcasts`(`created_at`);
CREATE INDEX `idx_telegram_broadcasts_deleted_at` ON `telegram_broadcasts`(`deleted_at`);
CREATE INDEX `idx_telegram_broadcasts_recipient_type` ON `telegram_broadcasts`(`recipient_type`);
CREATE INDEX `idx_telegram_broadcasts_status` ON `telegram_broadcasts`(`status`);
CREATE INDEX `idx_telegram_broadcasts_updated_at` ON `telegram_broadcasts`(`updated_at`);
CREATE INDEX `idx_telegram_business_events_bot_id` ON `telegram_business_events`(`bot_id`);
CREATE UNIQUE INDEX `idx_telegram_business_events_event_key` ON `telegram_business_events`(`event_key`);
CREATE INDEX `idx_telegram_business_events_lease_until` ON `telegram_business_events`(`lease_until`);
CREATE INDEX `idx_telegram_business_events_order_id` ON `telegram_business_events`(`order_id`);
CREATE INDEX `idx_telegram_business_ready` ON `telegram_business_events`(`status`,`available_at`);
CREATE UNIQUE INDEX `idx_telegram_history_binding` ON `telegram_history_grants`(`bot_id`,`previous_user_id`,`identity_id`);
CREATE INDEX `idx_telegram_history_grants_current_user_id` ON `telegram_history_grants`(`current_user_id`);
CREATE INDEX `idx_telegram_history_grants_status` ON `telegram_history_grants`(`status`);
CREATE INDEX `idx_telegram_history_grants_telegram_user_id` ON `telegram_history_grants`(`telegram_user_id`);
CREATE UNIQUE INDEX `idx_telegram_order_operations_operation_key` ON `telegram_order_operations`(`operation_key`);
CREATE INDEX `idx_telegram_order_sources_bot_id` ON `telegram_order_sources`(`bot_id`);
CREATE UNIQUE INDEX `idx_telegram_order_sources_order_id` ON `telegram_order_sources`(`order_id`);
CREATE INDEX `idx_telegram_order_sources_user_id` ON `telegram_order_sources`(`user_id`);
CREATE INDEX `idx_user_login_logs_client_ip` ON `user_login_logs`(`client_ip`);
CREATE INDEX `idx_user_login_logs_created_at` ON `user_login_logs`(`created_at`);
CREATE INDEX `idx_user_login_logs_email` ON `user_login_logs`(`email`);
CREATE INDEX `idx_user_login_logs_fail_reason` ON `user_login_logs`(`fail_reason`);
CREATE INDEX `idx_user_login_logs_login_source` ON `user_login_logs`(`login_source`);
CREATE INDEX `idx_user_login_logs_request_id` ON `user_login_logs`(`request_id`);
CREATE INDEX `idx_user_login_logs_status` ON `user_login_logs`(`status`);
CREATE INDEX `idx_user_login_logs_user_id` ON `user_login_logs`(`user_id`);
CREATE INDEX `idx_user_oauth_identities_created_at` ON `user_oauth_identities`(`created_at`);
CREATE INDEX `idx_user_oauth_identities_updated_at` ON `user_oauth_identities`(`updated_at`);
CREATE INDEX `idx_user_oauth_identities_user_id` ON `user_oauth_identities`(`user_id`);
CREATE UNIQUE INDEX `idx_user_oauth_identity_user_provider` ON `user_oauth_identities`(`user_id`,`provider`);
CREATE INDEX `idx_users_created_at` ON `users`(`created_at`);
CREATE INDEX `idx_users_deleted_at` ON `users`(`deleted_at`);
CREATE UNIQUE INDEX `idx_users_email` ON `users`(`email`);
CREATE INDEX `idx_users_invite_code_id` ON `users`(`invite_code_id`);
CREATE INDEX `idx_users_token_invalid_before` ON `users`(`token_invalid_before`);
CREATE INDEX `idx_users_totp_enabled_at` ON `users`(`totp_enabled_at`);
CREATE INDEX `idx_users_updated_at` ON `users`(`updated_at`);
CREATE INDEX `idx_wallet_accounts_created_at` ON `wallet_accounts`(`created_at`);
CREATE INDEX `idx_wallet_accounts_deleted_at` ON `wallet_accounts`(`deleted_at`);
CREATE INDEX `idx_wallet_accounts_updated_at` ON `wallet_accounts`(`updated_at`);
CREATE UNIQUE INDEX `idx_wallet_accounts_user_id` ON `wallet_accounts`(`user_id`);
CREATE INDEX `idx_wallet_recharge_orders_channel_id` ON `wallet_recharge_orders`(`channel_id`);
CREATE INDEX `idx_wallet_recharge_orders_created_at` ON `wallet_recharge_orders`(`created_at`);
CREATE INDEX `idx_wallet_recharge_orders_deleted_at` ON `wallet_recharge_orders`(`deleted_at`);
CREATE INDEX `idx_wallet_recharge_orders_paid_at` ON `wallet_recharge_orders`(`paid_at`);
CREATE UNIQUE INDEX `idx_wallet_recharge_orders_payment_id` ON `wallet_recharge_orders`(`payment_id`);
CREATE UNIQUE INDEX `idx_wallet_recharge_orders_recharge_no` ON `wallet_recharge_orders`(`recharge_no`);
CREATE INDEX `idx_wallet_recharge_orders_status` ON `wallet_recharge_orders`(`status`);
CREATE INDEX `idx_wallet_recharge_orders_updated_at` ON `wallet_recharge_orders`(`updated_at`);
CREATE INDEX `idx_wallet_recharge_orders_user_id` ON `wallet_recharge_orders`(`user_id`);
CREATE INDEX `idx_wallet_transactions_created_at` ON `wallet_transactions`(`created_at`);
CREATE INDEX `idx_wallet_transactions_deleted_at` ON `wallet_transactions`(`deleted_at`);
CREATE INDEX `idx_wallet_transactions_direction` ON `wallet_transactions`(`direction`);
CREATE INDEX `idx_wallet_transactions_operator_admin_id` ON `wallet_transactions`(`operator_admin_id`);
CREATE INDEX `idx_wallet_transactions_order_id` ON `wallet_transactions`(`order_id`);
CREATE UNIQUE INDEX `idx_wallet_transactions_reference` ON `wallet_transactions`(`reference`);
CREATE INDEX `idx_wallet_transactions_type` ON `wallet_transactions`(`type`);
CREATE INDEX `idx_wallet_transactions_updated_at` ON `wallet_transactions`(`updated_at`);
CREATE INDEX `idx_wallet_transactions_user_id` ON `wallet_transactions`(`user_id`);
CREATE UNIQUE INDEX `uidx_procurement_local_order` ON `procurement_orders`(`local_order_id`);
INSERT INTO "schema_migrations" ("version","completed_at") VALUES (9,'synthetic-v9-baseline');
INSERT INTO "settings" ("key","value_json") VALUES ('migration/category_parent_v1','{"done":true,"migrated_at":"2026-10-30T00:00:00Z"}');
INSERT INTO "settings" ("key","value_json") VALUES ('migration/manual_stock_remaining_v1','{"done":true,"migrated_at":"2026-10-30T00:00:00Z"}');
INSERT INTO "settings" ("key","value_json") VALUES ('migration/order_item_original_price_v1','{"done":true,"migrated_at":"2026-10-30T00:00:00Z"}');
INSERT INTO "settings" ("key","value_json") VALUES ('migration/order_refund_payment_fee_v1','{"done":true,"migrated_at":"2026-10-30T00:00:00Z","migrated_count":0}');
INSERT INTO "settings" ("key","value_json") VALUES ('migration/payment_channel_bepusdt_config_v2','{"done":true,"migrated_at":"2026-10-30T00:00:00Z","migrated_count":0,"skipped_channel_ids":[]}');
INSERT INTO "settings" ("key","value_json") VALUES ('migration/payment_fee_policy_v1','{"done":true,"migrated_at":"2026-10-30T00:00:00Z"}');
INSERT INTO "settings" ("key","value_json") VALUES ('migration/payment_provider_bepusdt_rename_v1','{"done":true,"migrated_at":"2026-10-30T00:00:00Z"}');
INSERT INTO "settings" ("key","value_json") VALUES ('migration/product_sku_v1','{"done":true,"migrated_at":"2026-10-30T00:00:00Z"}');
