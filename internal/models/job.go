package models

import (
	"time"

	"github.com/google/uuid"
)

type Job struct {
	Base
	// FIXED: Ubah dari uint ke uuid.UUID
	RestoreTestConfigID uuid.UUID         `gorm:"type:uuid;not null;index"`
	RestoreTestConfig   RestoreTestConfig `gorm:"foreignKey:RestoreTestConfigID"`

	Status                string `gorm:"default:'PENDING';index"`
	StartedAt             *time.Time
	FinishedAt            *time.Time
	DurationSeconds       int
	LogOutput             string `gorm:"type:text"`
	LastProcessedBackupID string
}

func (j *Job) MarkFinished(status string, logs string) {
	now := time.Now()
	j.FinishedAt = &now
	j.Status = status
	j.LogOutput = logs

	if j.StartedAt != nil {
		j.DurationSeconds = int(now.Sub(*j.StartedAt).Seconds())
	}
}
