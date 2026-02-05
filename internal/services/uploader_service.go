package services

import (
	"context"
	"databasus-checker/internal/models"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type UploaderService struct{}

func (s *UploaderService) UploadToStorage(storage models.StorageConfig, localFilePath string, remoteFileName string) error {
	file, err := os.Open(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	// Parse config JSON ke Map
	var cfg map[string]interface{}
	configBytes, _ := json.Marshal(storage.Config)
	json.Unmarshal(configBytes, &cfg)

	switch storage.Type {
	case "S3":
		return s.uploadS3(cfg, file, remoteFileName)
	case "FTP":
		return s.uploadFTP(cfg, file, remoteFileName)
	case "SFTP":
		return s.uploadSFTP(cfg, file, remoteFileName)
	default:
		return fmt.Errorf("storage type %s not implemented yet", storage.Type)
	}
}

// --- S3 Implementation ---
func (s *UploaderService) uploadS3(cfg map[string]interface{}, file *os.File, objectName string) error {
	endpoint, _ := cfg["endpoint"].(string)
	accessKey, _ := cfg["access_key"].(string)
	secretKey, _ := cfg["secret_key"].(string)
	bucket, _ := cfg["bucket"].(string)
	region, _ := cfg["region"].(string)
	prefix, _ := cfg["prefix"].(string) // Ambil Prefix
	useSSL := true
	
	// Default Endpoint AWS
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}

	// Initialize MinIO client object
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return err
	}

	// Logic Penggabungan Prefix + Filename
	finalObjectName := objectName
	if prefix != "" {
		// Gunakan Forward Slash (/) standar S3, bukan Backslash (\) ala Windows
		// Trim slash agar tidak double, lalu gabung
		cleanPrefix := strings.TrimSuffix(prefix, "/")
		finalObjectName = fmt.Sprintf("%s/%s", cleanPrefix, objectName)
	}

	// Upload
	info, err := file.Stat()
	if err != nil {
		return err
	}

	// Upload dengan finalObjectName (Prefix + Nama File)
	_, err = minioClient.PutObject(context.Background(), bucket, finalObjectName, file, info.Size(), minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	return err
}

// --- FTP Implementation ---
func (s *UploaderService) uploadFTP(cfg map[string]interface{}, file io.Reader, fileName string) error {
	host, _ := cfg["host"].(string)
	port := "21"
	if p, ok := cfg["port"].(float64); ok {
		port = fmt.Sprintf("%.0f", p)
	} else if p, ok := cfg["port"].(string); ok {
		port = p
	}
	
	user, _ := cfg["user"].(string)
	pass, _ := cfg["password"].(string)
	path, _ := cfg["path"].(string)

	c, err := ftp.Dial(fmt.Sprintf("%s:%s", host, port), ftp.DialWithTimeout(10*time.Second))
	if err != nil {
		return err
	}
	defer c.Quit()

	if err := c.Login(user, pass); err != nil {
		return err
	}

	// Change dir if needed (Prefix logic untuk FTP)
	if path != "" {
		// Coba buat directory jika belum ada (Opsional, tapi bagus untuk robustness)
		_ = c.MakeDir(path)
		if err := c.ChangeDir(path); err != nil {
			return fmt.Errorf("failed to change ftp dir: %v", err)
		}
	}

	return c.Stor(fileName, file)
}

// --- SFTP Implementation ---
func (s *UploaderService) uploadSFTP(cfg map[string]interface{}, file io.Reader, fileName string) error {
	host, _ := cfg["host"].(string)
	port := "22"
	if p, ok := cfg["port"].(float64); ok {
		port = fmt.Sprintf("%.0f", p)
	} else if p, ok := cfg["port"].(string); ok {
		port = p
	}
	
	user, _ := cfg["user"].(string)
	pass, _ := cfg["password"].(string)
	key, _ := cfg["private_key"].(string)
	remotePath, _ := cfg["path"].(string)

	// Setup Auth
	var authMethods []ssh.AuthMethod
	if key != "" {
		signer, err := ssh.ParsePrivateKey([]byte(key))
		if err == nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}
	}
	if pass != "" {
		authMethods = append(authMethods, ssh.Password(pass))
	}

	sshConfig := &ssh.ClientConfig{
		User: user,
		Auth: authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Simplifikasi untuk internal tool
		Timeout: 10 * time.Second,
	}

	sshClient, err := ssh.Dial("tcp", fmt.Sprintf("%s:%s", host, port), sshConfig)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return err
	}
	defer sftpClient.Close()

	// Logic Penggabungan Path + Filename (Prefix logic untuk SFTP)
	finalPath := fileName
	if remotePath != "" {
		// Pastikan folder ada
		_ = sftpClient.MkdirAll(remotePath)
		finalPath = filepath.Join(remotePath, fileName)
	}

	dstFile, err := sftpClient.Create(finalPath)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = dstFile.ReadFrom(file)
	return err
}
