package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

// Helper untuk menyimpan Array string sebagai JSON di Postgres
type StringArray []string

func (a *StringArray) Scan(value interface{}) error {
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}
	return json.Unmarshal(bytes, a)
}

func (a StringArray) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "[]", nil
	}
	return json.Marshal(a)
}

type RestoreTestConfig struct {
	Base
	Name                  string `gorm:"not null"`
	WorkspaceID           string `gorm:"not null"`
	DatabasusDatabaseID   string `gorm:"not null;uniqueIndex"`
	DatabasusDatabaseName string

	// Scripts
	PreRestoreScript  string `gorm:"type:text"`
	PostRestoreScript string `gorm:"type:text"`

	// Relations (Changed to Array for Multiple Selection)
	StorageIDs      StringArray `gorm:"type:jsonb"` // Stores ["uuid-1", "uuid-2"]
	NotificationIDs StringArray `gorm:"type:jsonb"` // Stores ["uuid-1", "uuid-2"]

	// State Polling
	LastProcessedBackupID string
}
