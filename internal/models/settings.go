package models

import "gorm.io/gorm"

type AppSettings struct {
	Base
	DatabasusURL      string
	DatabasusUser     string
	DatabasusPassword string
	AppTimezone       string
	DatabasusTimezone string
	LogRetentionDays  int
}

// Helper (Tetap sama)
func GetSettings(db *gorm.DB) AppSettings {
	var settings AppSettings
	if err := db.First(&settings).Error; err != nil {
		settings = AppSettings{
			DatabasusURL:      "http://host.docker.internal:4005",
			AppTimezone:       "Asia/Jakarta",
			DatabasusTimezone: "UTC",
			LogRetentionDays:  30,
		}
		db.Create(&settings)
	}
	return settings
}
