package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Base model dengan UUID
type Base struct {
	ID        uuid.UUID `gorm:"type:uuid;primary_key;"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// User model untuk login dashboard
type User struct {
	Base
	Email    string `gorm:"uniqueIndex;not null"`
	Password string `gorm:"not null"` // Bcrypt Hash
	Role     string `gorm:"default:'ADMIN'"`
}

// Hook untuk generate UUID sebelum create
func (base *Base) BeforeCreate(tx *gorm.DB) (err error) {
	base.ID = uuid.New()
	return
}
