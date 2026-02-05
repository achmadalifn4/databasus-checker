package models

// Kita gunakan JSONMap yang sama dengan StorageConfig
// Pastikan struct ini ada di package models

type NotificationConfig struct {
	Base
	Name   string  `gorm:"not null"`
	Type   string  `gorm:"not null"` // TELEGRAM, EMAIL
	Config JSONMap `gorm:"type:jsonb"`
}
