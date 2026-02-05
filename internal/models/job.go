package models

import (
	"time"

	"github.com/google/uuid"
)

type Job struct {
	Base
	// FIXED: Gunakan Pointer (*) agar bisa NULL di database
	RestoreTestConfigID *uuid.UUID         `gorm:"type:uuid;index"` 
	RestoreTestConfig   RestoreTestConfig  `gorm:"foreignKey:RestoreTestConfigID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`

	// FIXED: Simpan nama test disini (Snapshot) agar kalau Config dihapus, nama tetap ada
	TestSnapshotName string `gorm:"type:varchar(255)"`

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
