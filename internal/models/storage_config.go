package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

// Helper untuk menyimpan Map (JSON Object) di Database
type JSONMap map[string]interface{}

func (m *JSONMap) Scan(value interface{}) error {
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}
	return json.Unmarshal(bytes, m)
}

func (m JSONMap) Value() (driver.Value, error) {
	return json.Marshal(m)
}

type StorageConfig struct {
	Base
	Name   string  `gorm:"not null"`
	Type   string  `gorm:"not null"` // S3, NAS, FTP, SFTP, RCLONE
	Config JSONMap `gorm:"type:jsonb"`
}
