package services

import (
	"databasus-checker/internal/database"
	"databasus-checker/internal/models"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type QueueService struct{}

func (s *QueueService) Enqueue(testID string) (*models.Job, error) {
	parsedID, err := uuid.Parse(testID)
	if err != nil {
		return nil, errors.New("invalid test id format")
	}

	var count int64
	database.DB.Model(&models.Job{}).
		Where("restore_test_config_id = ? AND status IN ('PENDING', 'RUNNING')", parsedID).
		Count(&count)

	if count > 0 {
		return nil, errors.New("this test is already queued or running")
	}

	job := models.Job{
		RestoreTestConfigID: parsedID,
		Status:              "PENDING",
	}

	if err := database.DB.Create(&job).Error; err != nil {
		return nil, err
	}

	return &job, nil
}

func (s *QueueService) GetPendingJob() (*models.Job, error) {
	var job models.Job

	err := database.DB.Transaction(func(tx *gorm.DB) error {
		// 1. Cari ID Job yang pending (Hanya ambil ID saja biar ringan)
		// Kita Preload nanti di luar transaksi atau setelah dapat ID
		result := tx.Set("gorm:query_option", "FOR UPDATE SKIP LOCKED").
			Where("status = ?", "PENDING").
			Order("created_at asc").
			First(&job)

		if result.Error != nil {
			return result.Error
		}

		// 2. Update status langsung via Query SQL murni (Paling aman dari side-effect GORM)
		// "UPDATE jobs SET status='RUNNING', started_at=NOW() WHERE id = ?"
		now := time.Now()
		if err := tx.Model(&models.Job{}).Where("id = ?", job.ID).Updates(map[string]interface{}{
			"status":     "RUNNING",
			"started_at": now,
		}).Error; err != nil {
			return err
		}
		
		// Update struct lokal biar return-nya bener
		job.Status = "RUNNING"
		job.StartedAt = &now
		
		return nil
	})

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil // Silent
		}
		return nil, err
	}

	// 3. Setelah status running, baru kita load data lengkap (termasuk Config)
	// Ini aman karena kita cuma Read (First), tidak Save
	var fullJob models.Job
	if err := database.DB.Preload("RestoreTestConfig").First(&fullJob, "id = ?", job.ID).Error; err != nil {
		return nil, err
	}

	return &fullJob, nil
}

func (s *QueueService) UpdateJob(job *models.Job) {
	// PENTING: Gunakan Select spesifik kolom untuk update hasil akhir
	// Jangan pakai Omit, lebih baik whitelist kolom yang mau diubah
	database.DB.Model(job).Select("status", "finished_at", "duration_seconds", "log_output", "last_processed_backup_id").Updates(job)
}

func (s *QueueService) GetAllJobs() ([]models.Job, error) {
	var jobs []models.Job
	err := database.DB.Preload("RestoreTestConfig").Order("created_at desc").Limit(50).Find(&jobs).Error
	return jobs, err
}
