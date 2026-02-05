package services

import (
	"databasus-checker/internal/database"
	"databasus-checker/internal/models"
	"errors"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AuthService struct{}

// Cek apakah sistem baru (belum ada user)
func (s *AuthService) IsFreshInstall() bool {
	var count int64
	database.DB.Model(&models.User{}).Count(&count)
	return count == 0
}

// Create admin pertama kali via Wizard
func (s *AuthService) CreateFirstAdmin(email, password string) error {
	if !s.IsFreshInstall() {
		return errors.New("installation already completed")
	}

	hashed, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	user := models.User{
		Email:    email,
		Password: string(hashed),
		Role:     "ADMIN",
	}

	return database.DB.Create(&user).Error
}

// Login logic
func (s *AuthService) Authenticate(email, password string) (*models.User, error) {
	var user models.User
	err := database.DB.Where("email = ?", email).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("invalid credentials")
	}

	err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password))
	if err != nil {
		return nil, errors.New("invalid credentials")
	}

	return &user, nil
}
