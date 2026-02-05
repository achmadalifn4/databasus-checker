package worker

import (
	"databasus-checker/internal/models"
	"databasus-checker/internal/services"
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Worker struct {
	QueueService    services.QueueService
	DatabasusClient services.DatabasusClient
	DockerService   services.DockerService
}

func NewWorker() *Worker {
	return &Worker{
		QueueService:    services.QueueService{},
		DatabasusClient: services.DatabasusClient{},
		DockerService:   services.DockerService{},
	}
}

func (w *Worker) Start() {
	log.Println("Background Worker Started... (Polling every 5s)")

	go func() {
		for {
			job, err := w.QueueService.GetPendingJob()
			if err != nil {
				log.Printf("Worker DB Error: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}

			if job == nil {
				time.Sleep(5 * time.Second)
				continue
			}

			log.Printf("Worker: Processing Job ID %s (Test: %s)", job.ID, job.RestoreTestConfig.Name)
			w.processJob(job)
		}
	}()
}

func (w *Worker) processJob(job *models.Job) {
	var logs strings.Builder

	logPrint := func(format string, a ...interface{}) {
		msg := fmt.Sprintf(format, a...)
		timestamp := time.Now().Format("15:04:05")
		logs.WriteString(fmt.Sprintf("[%s] %s\n", timestamp, msg))
		log.Printf("[Job %s] %s", job.ID.String()[:8], msg)
	}

	logPrint("Starting job execution...")

	// 1. Get Latest Backup
	logPrint("Fetching latest backup for DB ID: %s", job.RestoreTestConfig.DatabasusDatabaseID)
	backup, err := w.DatabasusClient.GetLatestBackup(job.RestoreTestConfig.DatabasusDatabaseID)
	if err != nil {
		logPrint("ERROR: Failed to get backup: %v", err)
		job.MarkFinished("FAILED", logs.String())
		w.QueueService.UpdateJob(job)
		return
	}
	logPrint("Found backup ID: %s (Status: %s)", backup.ID, backup.Status)

	// 2. Spawn Ephemeral Postgres Container
	logPrint("Spawning temporary Postgres container...")
	ephemeralDB, err := w.DockerService.SpawnPostgres(job.ID.String())
	if err != nil {
		logPrint("ERROR: Failed to spawn docker: %v", err)
		job.MarkFinished("FAILED", logs.String())
		w.QueueService.UpdateJob(job)
		return
	}
	logPrint("Container Created. Port: %d, DB: %s, User: %s", ephemeralDB.Port, ephemeralDB.DBName, ephemeralDB.User)

	// Cleanup (Stop Docker) di akhir
	defer func() {
		logPrint("Cleaning up: Stopping container %s...", ephemeralDB.ContainerID)
		if err := w.DockerService.StopContainer(ephemeralDB.ContainerID); err != nil {
			logPrint("WARN: Failed to stop container: %v", err)
		}
	}()

	// 3. Wait for Postgres to be ready
	logPrint("Waiting for Postgres to be ready...")
	dbDSN := fmt.Sprintf("host=host.docker.internal user=%s password=%s dbname=%s port=%d sslmode=disable", 
		ephemeralDB.User, ephemeralDB.Password, ephemeralDB.DBName, ephemeralDB.Port)
	
	// Retry loop connect
	var targetDB *gorm.DB
	for i := 0; i < 10; i++ {
		time.Sleep(2 * time.Second)
		targetDB, err = gorm.Open(postgres.Open(dbDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err == nil {
			sqlDB, _ := targetDB.DB()
			if err := sqlDB.Ping(); err == nil {
				logPrint("Connected to temporary database.")
				break
			}
		}
		if i == 9 {
			logPrint("ERROR: Timed out waiting for temp database.")
			job.MarkFinished("FAILED", logs.String())
			w.QueueService.UpdateJob(job)
			return
		}
	}

	// 4. Trigger Restore API
	logPrint("Triggering Restore API...")
	// Target host "host.docker.internal" agar Databasus (di container lain) bisa tembak ke Host Port container baru kita
	err = w.DatabasusClient.TriggerRestore(backup.ID, "host.docker.internal", ephemeralDB.Port, ephemeralDB.User, ephemeralDB.Password, ephemeralDB.DBName)
	if err != nil {
		logPrint("ERROR: Restore API call failed: %v", err)
		job.MarkFinished("FAILED", logs.String())
		w.QueueService.UpdateJob(job)
		return
	}

	// 5. Wait for Restore Data
	// Estimasi waktu restore. Idealnya polling, tapi API tidak return Job ID restore.
	logPrint("Waiting for restore data (30 seconds)...")
	time.Sleep(30 * time.Second)

	// 6. Validation Script
	if job.RestoreTestConfig.PostRestoreScript != "" {
		logPrint("Running Post-Restore Validation...")
		if err := targetDB.Exec(job.RestoreTestConfig.PostRestoreScript).Error; err != nil {
			logPrint("VALIDATION FAILED: %v", err)
			job.MarkFinished("FAILED", logs.String())
			w.QueueService.UpdateJob(job)
			return
		}
		logPrint("Validation Passed.")
	} else {
		logPrint("No validation script. Skip.")
	}

	// 7. Success
	logPrint("Restore Test Completed Successfully.")
	job.MarkFinished("SUCCESS", logs.String())
	job.LastProcessedBackupID = backup.ID
	w.QueueService.UpdateJob(job)
}
