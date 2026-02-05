package worker

import (
	"databasus-checker/internal/database"
	"databasus-checker/internal/models"
	"databasus-checker/internal/services"
	"databasus-checker/internal/utils"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	UploaderService services.UploaderService
}

func NewWorker() *Worker {
	return &Worker{
		QueueService:    services.QueueService{},
		DatabasusClient: services.DatabasusClient{},
		DockerService:   services.DockerService{},
		UploaderService: services.UploaderService{},
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

	sendNotification := func(isSuccess bool, message string) {
		var notifs []models.NotificationConfig
		notificationIDs := []string(job.RestoreTestConfig.NotificationIDs)

		if len(notificationIDs) > 0 {
			if err := database.DB.Where("id IN ?", notificationIDs).Find(&notifs).Error; err != nil {
				logPrint("ERROR: Failed to fetch notification configs: %v", err)
				return
			}
			
			status := "FAILED"
			if isSuccess {
				status = "SUCCESS"
			}
			
			fullMsg := fmt.Sprintf("[%s] Restore Test: %s\n\n%s", status, job.RestoreTestConfig.Name, message)

			for _, n := range notifs {
				logPrint("Sending notification to %s (%s)...", n.Name, n.Type)
				cfg := n.Config
				
				if n.Type == "TELEGRAM" {
					token, _ := cfg["bot_token"].(string)
					chatID, _ := cfg["chat_id"].(string)
					utils.SendTelegram(token, chatID, fullMsg)
				} else if n.Type == "EMAIL" {
					host, _ := cfg["host"].(string)
					portStr := fmt.Sprintf("%v", cfg["port"])
					port := 587
					fmt.Sscanf(portStr, "%d", &port)
					
					utils.SendEmail(
						host, port,
						cfg["user"].(string), cfg["password"].(string),
						cfg["from_email"].(string), cfg["to_email"].(string),
						fmt.Sprintf("Databasus Checker: %s", status),
						fullMsg,
					)
				}
			}
		}
	}

	logPrint("Starting job execution...")

	// 1. Get Latest Backup
	logPrint("Fetching latest backup for DB ID: %s", job.RestoreTestConfig.DatabasusDatabaseID)
	backup, err := w.DatabasusClient.GetLatestBackup(job.RestoreTestConfig.DatabasusDatabaseID)
	if err != nil {
		logPrint("ERROR: Failed to get backup: %v", err)
		job.MarkFinished("FAILED", logs.String())
		w.QueueService.UpdateJob(job)
		sendNotification(false, fmt.Sprintf("Failed to fetch backup: %v", err))
		return
	}
	logPrint("Found backup ID: %s (Status: %s)", backup.ID, backup.Status)

	// 2. Fetch DB Version
	logPrint("Fetching Database Version info...")
	pgVersion, err := w.DatabasusClient.GetDatabaseVersion(job.RestoreTestConfig.WorkspaceID, job.RestoreTestConfig.DatabasusDatabaseID)
	if err != nil {
		logPrint("WARN: Failed to get version, defaulting to 15. Error: %v", err)
		pgVersion = "15"
	}
	logPrint("Target PostgreSQL Version: %s", pgVersion)

	// 3. Spawn Docker
	logPrint("Spawning temporary Postgres container (Tag: postgres:%s-alpine)...", pgVersion)
	ephemeralDB, err := w.DockerService.SpawnPostgres(job.ID.String(), pgVersion)
	if err != nil {
		logPrint("ERROR: Failed to spawn docker: %v", err)
		job.MarkFinished("FAILED", logs.String())
		w.QueueService.UpdateJob(job)
		sendNotification(false, fmt.Sprintf("Failed to spawn docker: %v", err))
		return
	}
	logPrint("Container Created. Port: %d, DB: %s, User: %s", ephemeralDB.Port, ephemeralDB.DBName, ephemeralDB.User)

	defer func() {
		logPrint("Cleaning up: Stopping container %s...", ephemeralDB.ContainerID)
		if err := w.DockerService.StopContainer(ephemeralDB.ContainerID); err != nil {
			logPrint("WARN: Failed to stop container: %v", err)
		}
	}()

	// 4. Wait for Postgres
	logPrint("Waiting for Postgres to be ready...")
	dbDSN := fmt.Sprintf("host=host.docker.internal user=%s password=%s dbname=%s port=%d sslmode=disable TimeZone=UTC", 
		ephemeralDB.User, ephemeralDB.Password, ephemeralDB.DBName, ephemeralDB.Port)
	
	var targetDB *gorm.DB
	for i := 0; i < 15; i++ { 
		time.Sleep(2 * time.Second)
		targetDB, err = gorm.Open(postgres.Open(dbDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err == nil {
			sqlDB, dbErr := targetDB.DB()
			if dbErr == nil {
				if pingErr := sqlDB.Ping(); pingErr == nil {
					logPrint("Connected to temporary database.")
					break
				}
			}
		}
		if i == 14 {
			logPrint("ERROR: Timed out waiting for temp database.")
			job.MarkFinished("FAILED", logs.String())
			w.QueueService.UpdateJob(job)
			sendNotification(false, "Timeout waiting for temporary database.")
			return
		}
	}

	// 5. Trigger Restore
	logPrint("Triggering Restore API...")
	err = w.DatabasusClient.TriggerRestore(backup.ID, "host.docker.internal", ephemeralDB.Port, ephemeralDB.User, ephemeralDB.Password, ephemeralDB.DBName)
	if err != nil {
		logPrint("ERROR: Restore API call failed: %v", err)
		job.MarkFinished("FAILED", logs.String())
		w.QueueService.UpdateJob(job)
		sendNotification(false, fmt.Sprintf("Restore API Failed: %v", err))
		return
	}

	// 6. Wait Data
	logPrint("Waiting for restore data (30 seconds)...")
	time.Sleep(30 * time.Second)

	// 7. Validation
	if job.RestoreTestConfig.PostRestoreScript != "" {
		logPrint("Running Post-Restore Validation...")
		if err := targetDB.Exec(job.RestoreTestConfig.PostRestoreScript).Error; err != nil {
			logPrint("VALIDATION FAILED: %v", err)
			job.MarkFinished("FAILED", logs.String())
			w.QueueService.UpdateJob(job)
			sendNotification(false, fmt.Sprintf("Validation SQL Failed: %v", err))
			return
		}
		logPrint("Validation Passed.")
	}

	// 8. Upload to Storage
	storageIDs := []string(job.RestoreTestConfig.StorageIDs)
	finalStatus := "SUCCESS"
	finalMessage := fmt.Sprintf("Backup %s validated successfully.", backup.ID)

	if len(storageIDs) > 0 {
		logPrint("Starting Upload Process...")
		
		searchPattern := filepath.Join(os.Getenv("BACKUP_PATH"), backup.ID + "*")
		matches, err := filepath.Glob(searchPattern)
		
		if err != nil || len(matches) == 0 {
			logPrint("ERROR: Backup file not found locally using pattern: %s", searchPattern)
			logPrint("Ensure Databasus and Checker share the volume and Databasus has finished writing.")
			
			finalStatus = "FAILED"
			finalMessage = "Restore success but Upload failed: Local file not found."
		} else {
			localFilePath := matches[0]
			logPrint("Found local backup file: %s", localFilePath)

			timestamp := backup.CreatedAt.Format("20060102_150405")
			remoteFileName := fmt.Sprintf("%s-%s-backup.dump", job.RestoreTestConfig.DatabasusDatabaseName, timestamp)
			
			logPrint("Uploading as: %s", remoteFileName)

			var storages []models.StorageConfig
			if err := database.DB.Where("id IN ?", storageIDs).Find(&storages).Error; err != nil {
				logPrint("ERROR: Failed to fetch storage configs: %v", err)
				finalStatus = "FAILED"
			} else {
				for _, storage := range storages {
					logPrint("Uploading to %s (%s)...", storage.Name, storage.Type)
					if err := w.UploaderService.UploadToStorage(storage, localFilePath, remoteFileName); err != nil {
						logPrint("ERROR: Upload failed: %v", err)
						finalStatus = "FAILED"
						finalMessage = fmt.Sprintf("Restore success but Upload to %s failed: %v", storage.Name, err)
					} else {
						logPrint("Upload Success.")
					}
				}
			}
		}
	}

	// 9. Finish
	logPrint("Process Completed with status: %s", finalStatus)
	job.MarkFinished(finalStatus, logs.String())
	job.LastProcessedBackupID = backup.ID
	w.QueueService.UpdateJob(job) // Update tabel jobs

	// FIXED: Update tabel Parent (RestoreTestConfig) agar ID muncul di list view
	if finalStatus == "SUCCESS" {
		if job.RestoreTestConfigID != nil {
			if err := database.DB.Model(&models.RestoreTestConfig{}).
				Where("id = ?", job.RestoreTestConfigID).
				Update("last_processed_backup_id", backup.ID).Error; err != nil {
				logPrint("WARN: Failed to update config last_processed_id: %v", err)
			}
		}
	}
	
	sendNotification(finalStatus == "SUCCESS", finalMessage)
}
