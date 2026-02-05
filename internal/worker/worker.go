package worker

import (
	"databasus-checker/internal/models"
	"databasus-checker/internal/services"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Worker struct {
	QueueService    services.QueueService
	DatabasusClient services.DatabasusClient
}

func NewWorker() *Worker {
	return &Worker{
		QueueService:    services.QueueService{},
		DatabasusClient: services.DatabasusClient{},
	}
}

func (w *Worker) Start() {
	log.Println("Background Worker Started... (Polling every 5s)")

	go func() {
		for {
			job, err := w.QueueService.GetPendingJob()
			if err != nil {
				// Log error serius
				log.Printf("Worker DB Error: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}

			if job == nil {
				// Tidak ada job, sleep (Silent)
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

	// Helper untuk log ke memory string & console
	logPrint := func(format string, a ...interface{}) {
		msg := fmt.Sprintf(format, a...)
		timestamp := time.Now().Format("15:04:05")
		logs.WriteString(fmt.Sprintf("[%s] %s\n", timestamp, msg))
		log.Printf("[Job %s] %s", job.ID.String()[:8], msg)
	}

	logPrint("Starting job execution...")

	// 1. Get Latest Backup from Databasus API
	logPrint("Fetching latest backup info for Database ID: %s...", job.RestoreTestConfig.DatabasusDatabaseID)
	backup, err := w.DatabasusClient.GetLatestBackup(job.RestoreTestConfig.DatabasusDatabaseID)
	if err != nil {
		logPrint("ERROR: Failed to get backup: %v", err)
		job.MarkFinished("FAILED", logs.String())
		w.QueueService.UpdateJob(job)
		return
	}
	logPrint("Found valid backup ID: %s (Created: %s)", backup.ID, backup.CreatedAt.Format(time.RFC3339))

	// 2. Prepare Temp Database Credentials (Local Postgres)
	dbHost := os.Getenv("DB_HOST")
	dbPort := os.Getenv("DB_PORT")
	dbUser := os.Getenv("DB_USER")
	dbPass := os.Getenv("DB_PASSWORD")

	// Nama DB sementara (hapus dash dari UUID agar valid sebagai nama DB)
	cleanJobID := strings.ReplaceAll(job.ID.String(), "-", "")
	tempDBName := fmt.Sprintf("temp_%s", cleanJobID)

	// 3. Connect to Postgres Admin (untuk create DB)
	adminDSN := fmt.Sprintf("host=%s user=%s password=%s dbname=postgres port=%s sslmode=disable", dbHost, dbUser, dbPass, dbPort)
	adminDB, err := gorm.Open(postgres.Open(adminDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		logPrint("ERROR: Failed to connect to local postgres admin: %v", err)
		job.MarkFinished("FAILED", logs.String())
		w.QueueService.UpdateJob(job)
		return
	}

	// Create Temp Database
	logPrint("Creating temporary database: %s", tempDBName)
	// Safety: Drop if exists first
	adminDB.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", tempDBName))

	if err := adminDB.Exec(fmt.Sprintf("CREATE DATABASE %s", tempDBName)).Error; err != nil {
		logPrint("ERROR: Failed to create database: %v", err)
		job.MarkFinished("FAILED", logs.String())
		w.QueueService.UpdateJob(job)
		return
	}

	// --- DEFER CLEANUP ---
	// Pastikan DB dihapus di akhir, sukses atau gagal
	defer func() {
		logPrint("Cleaning up resources...")
		// Tutup koneksi adminDB yang mungkin menggantung
		sqlDB, _ := adminDB.DB()
		sqlDB.Close()

		// Buka koneksi baru khusus untuk drop (agar tidak ada sesi aktif)
		cleanDB, _ := gorm.Open(postgres.Open(adminDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})

		// Force drop (putuskan koneksi lain jika ada)
		cleanDB.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", tempDBName))
		logPrint("Temporary database dropped.")
	}()

	// 4. Connect to the NEW Temp Database
	tempDSN := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=disable", dbHost, dbUser, dbPass, tempDBName, dbPort)
	tempDB, err := gorm.Open(postgres.Open(tempDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		logPrint("ERROR: Failed to connect to temp db: %v", err)
		job.MarkFinished("FAILED", logs.String())
		w.QueueService.UpdateJob(job)
		return
	}

	// 5. Run Pre-Restore Script
	if job.RestoreTestConfig.PreRestoreScript != "" {
		logPrint("Running Pre-Restore SQL Script...")
		if err := tempDB.Exec(job.RestoreTestConfig.PreRestoreScript).Error; err != nil {
			logPrint("ERROR: Pre-restore script failed: %v", err)
			job.MarkFinished("FAILED", logs.String())
			w.QueueService.UpdateJob(job)
			return
		}
	}

	// 6. Trigger Restore API
	logPrint("Triggering Databasus Restore API...")
	portInt, _ := strconv.Atoi(dbPort)

	// Minta Databasus mengirim data ke DB Temp kita
	err = w.DatabasusClient.TriggerRestore(backup.ID, dbHost, portInt, dbUser, dbPass, tempDBName)
	if err != nil {
		logPrint("ERROR: Restore API call failed: %v", err)
		logPrint("Hint: Ensure Databasus Server can reach host '%s'", dbHost)
		job.MarkFinished("FAILED", logs.String())
		w.QueueService.UpdateJob(job)
		return
	}

	// Wait Loop (Sederhana: Sleep)
	// Karena kita tidak punya endpoint callback status restore, kita beri jeda waktu.
	// Jika backup besar, ini bisa jadi issue (nanti kita bahas handling file besar).
	logPrint("Waiting for restore process to complete (10s)...")
	time.Sleep(10 * time.Second)

	// 7. Run Post-Restore Script (Validation)
	if job.RestoreTestConfig.PostRestoreScript != "" {
		logPrint("Running Post-Restore (Validation) Script...")

		// Eksekusi script validasi
		if err := tempDB.Exec(job.RestoreTestConfig.PostRestoreScript).Error; err != nil {
			logPrint("VALIDATION FAILED: Script returned error: %v", err)
			job.MarkFinished("FAILED", logs.String())
			w.QueueService.UpdateJob(job)
			return
		}
		logPrint("Validation Script executed successfully.")
	} else {
		logPrint("No validation script provided. Skipping.")
	}

	// 8. Upload & Notify (Akan diisi di tahap selanjutnya)
	logPrint("Check passed. Ready for Upload & Notification.")

	// Update Job as Success
	job.MarkFinished("SUCCESS", logs.String())
	job.LastProcessedBackupID = backup.ID

	// Update Test Config last processed ID
	w.QueueService.UpdateJob(job)
	// (Opsional: Update RestoreTestConfig LastProcessedBackupID di DB)
}
