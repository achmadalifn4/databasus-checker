package services

import (
	"bytes"
	"databasus-checker/internal/database"
	"databasus-checker/internal/models"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type DatabasusClient struct{}

// --- Structs ---

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token string `json:"token"`
}

type WorkspaceDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type WorkspacesResponse struct {
	Workspaces []WorkspaceDTO `json:"workspaces"`
}

type DatabaseDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Structs untuk Backup & Restore
type BackupDTO struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Status    string    `json:"status"`
	FilePath  string    `json:"filePath"`
}

type BackupsResponse struct {
	Backups []BackupDTO `json:"backups"`
}

type RestorePayload struct {
	PostgresConfig map[string]interface{} `json:"postgresqlDatabase"`
}

// --- Logic ---

func (c *DatabasusClient) getToken(settings models.AppSettings) (string, error) {
	reqBody, _ := json.Marshal(LoginRequest{
		Email:    settings.DatabasusUser,
		Password: settings.DatabasusPassword,
	})

	resp, err := http.Post(settings.DatabasusURL+"/api/v1/users/signin", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", errors.New("failed to login to databasus: check credentials")
	}

	var loginResp LoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return "", err
	}

	return loginResp.Token, nil
}

func (c *DatabasusClient) GetWorkspaces() ([]WorkspaceDTO, error) {
	settings := models.GetSettings(database.DB)
	token, err := c.getToken(settings)
	if err != nil {
		return nil, err
	}

	req, _ := http.NewRequest("GET", settings.DatabasusURL+"/api/v1/workspaces", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("api error %d", resp.StatusCode)
	}

	var result WorkspacesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Workspaces, nil
}

func (c *DatabasusClient) GetDatabases(workspaceID string) ([]DatabaseDTO, error) {
	settings := models.GetSettings(database.DB)
	token, err := c.getToken(settings)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/api/v1/databases?workspace_id=%s", settings.DatabasusURL, workspaceID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, errors.New("failed to fetch databases")
	}

	var result []DatabaseDTO
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result, nil
}

// Test Storage Connection (Proxy)
func (c *DatabasusClient) TestStorageConnection(payload map[string]interface{}) error {
	settings := models.GetSettings(database.DB)
	token, err := c.getToken(settings)
	if err != nil {
		return err
	}

	reqBody, _ := json.Marshal(payload)

	url := settings.DatabasusURL + "/api/v1/storages/direct-test"
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(reqBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		var errResp map[string]interface{}
		if json.Unmarshal(body, &errResp) == nil {
			if msg, ok := errResp["message"].(string); ok {
				return errors.New(msg)
			}
		}
		return fmt.Errorf("connection failed (Status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// --- NEW WORKER METHODS ---

func (c *DatabasusClient) GetLatestBackup(databaseID string) (*BackupDTO, error) {
	settings := models.GetSettings(database.DB)
	token, err := c.getToken(settings)
	if err != nil {
		return nil, err
	}

	// FIXED: Urutkan berdasarkan created_at desc untuk ambil yg terbaru
	url := fmt.Sprintf("%s/api/v1/backups?database_id=%s&limit=1&sort=created_at:desc", settings.DatabasusURL, databaseID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to fetch backups (status: %d)", resp.StatusCode)
	}

	var result BackupsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if len(result.Backups) == 0 {
		return nil, errors.New("no backups found for this database")
	}

	// FIXED: Cek status COMPLETED (sesuai curl user)
	if result.Backups[0].Status != "COMPLETED" && result.Backups[0].Status != "SUCCESS" {
		return nil, fmt.Errorf("latest backup status is %s (not COMPLETED)", result.Backups[0].Status)
	}

	return &result.Backups[0], nil
}

func (c *DatabasusClient) TriggerRestore(backupID string, targetHost string, targetPort int, targetUser, targetPass, targetDB string) error {
	settings := models.GetSettings(database.DB)
	token, err := c.getToken(settings)
	if err != nil {
		return err
	}

	payload := RestorePayload{
		PostgresConfig: map[string]interface{}{
			"host":     targetHost,
			"port":     targetPort,
			"username": targetUser,
			"password": targetPass,
			"database": targetDB,
			"sslmode":  "disable",
		},
	}

	reqBody, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/api/v1/restores/%s/restore", settings.DatabasusURL, backupID)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(reqBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	// Restore trigger should be fast
	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("restore api failed (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}
