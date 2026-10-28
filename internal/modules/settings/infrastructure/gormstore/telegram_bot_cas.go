package settingsstore

import (
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"gorm.io/gorm/clause"
)

// CompareAndSwapTelegramBotConfig atomically protects the whole document across processes.
func (store *Store) CompareAndSwapTelegramBotConfig(key string, expected int, value jsonmap.JSON) (bool, error) {
	// Seed absent rows without overwriting a concurrent writer.
	if err := store.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&SettingRecord{Key: key, ValueJSON: jsonmap.JSON{"config_version": 0}}).Error; err != nil {
		return false, err
	}
	version := "COALESCE(json_extract(value_json, '$.config_version'), 0) = ?"
	if store.db.Dialector.Name() == "postgres" {
		version = "COALESCE((value_json->>'config_version')::bigint, 0) = ?"
	}
	result := store.db.Model(&SettingRecord{}).Where(map[string]interface{}{"key": key}).Where(version, expected).Update("value_json", value)
	return result.RowsAffected == 1, result.Error
}
