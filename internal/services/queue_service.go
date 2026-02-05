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

	// Cek apakah config ada (sekalian ambil Namanya untuk snapshot)
	var config models.RestoreTestConfig
	if err := database.DB.First(&config, "id = ?", parsedID).Error; err != nil {
		return nil, errors.New("restore test config not found")
	}

	// Cek duplikasi job pending
	var count int64
	database.DB.Model(&models.Job{}).
		Where("restore_test_config_id = ? AND status IN ('PENDING', 'RUNNING')", parsedID).
		Count(&count)

	if count > 0 {
		return nil, errors.New("this test is already queued or running")
	}

	job := models.Job{
		RestoreTestConfigID: &parsedID,     // Pointer
		TestSnapshotName:    config.Name,   // Snapshot Nama
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
		result := tx.Set("gorm:query_option", "FOR UPDATE SKIP LOCKED").
			Where("status = ?", "PENDING").
			Order("created_at asc").
			First(&job)

		if result.Error != nil {
			return result.Error
		}

		now := time.Now()
		if err := tx.Model(&models.Job{}).Where("id = ?", job.ID).Updates(map[string]interface{}{
			"status":     "RUNNING",
			"started_at": now,
		}).Error; err != nil {
			return err
		}
		
		job.Status = "RUNNING"
		job.StartedAt = &now
		return nil
	})

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	// Preload config. Jika config sudah dihapus (NULL), GORM tidak akan error, fieldnya kosong
	var fullJob models.Job
	if err := database.DB.Preload("RestoreTestConfig").First(&fullJob, "id = ?", job.ID).Error; err != nil {
		return nil, err
	}

	return &fullJob, nil
}

func (s *QueueService) UpdateJob(job *models.Job) {
	database.DB.Model(job).Select("status", "finished_at", "duration_seconds", "log_output", "last_processed_backup_id").Updates(job)
}

func (s *QueueService) GetActiveJobs() ([]models.Job, error) {
	var jobs []models.Job
	// Tidak perlu Preload Config lagi karena kita pakai Snapshot Name untuk display
	err := database.DB.Where("status IN ?", []string{"PENDING", "RUNNING"}).
		Order("created_at asc").
		Find(&jobs).Error
	return jobs, err
}

func (s *QueueService) GetJobHistory(limit int) ([]models.Job, error) {
	var jobs []models.Job
	err := database.DB.Where("status IN ?", []string{"SUCCESS", "FAILED"}).
		Order("finished_at desc").
		Limit(limit).
		Find(&jobs).Error
	return jobs, err
}
