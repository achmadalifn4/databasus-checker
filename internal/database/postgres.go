package database

import (
	"databasus-checker/internal/models"
	"fmt"
	"log"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func Connect() {
	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=disable TimeZone=UTC",
		os.Getenv("DB_HOST"),
		os.Getenv("DB_USER"),
		os.Getenv("DB_PASSWORD"),
		os.Getenv("DB_NAME"),
		os.Getenv("DB_PORT"),
	)

	var err error
	// Retry logic: Tunggu database container siap
	for i := 0; i < 10; i++ {
		DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Info),
		})
		if err == nil {
			break
		}
		log.Printf("Failed to connect to DB, retrying in 2 seconds... (%d/10)", i+1)
		time.Sleep(2 * time.Second)
	}

	if err != nil {
		log.Fatal("Could not connect to database after retries: ", err)
	}

	log.Println("Successfully connected to internal database")
	migrate()
}

func migrate() {
	// Auto migrate schema
	err := DB.AutoMigrate(
		&models.User{},
		&models.AppSettings{},
		&models.RestoreTestConfig{},
		&models.StorageConfig{},
		&models.NotificationConfig{},
		&models.Job{},
		// Nanti kita tambah models lain disini (Queue, StorageConfig, dll)
	)
	if err != nil {
		log.Fatal("Migration failed: ", err)
	}
}
