package services

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type DockerService struct{}

type EphemeralDB struct {
	ContainerID string
	Port        int
	User        string
	Password    string
	DBName      string
	Version     string
}

// Helper: Cari port host yang kosong
func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Helper: Random String
func randomString(n int) string {
	var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// FIXED: Tambahkan parameter pgVersion
func (s *DockerService) SpawnPostgres(jobID string, pgVersion string) (*EphemeralDB, error) {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %v", err)
	}

	// Fallback version
	if pgVersion == "" {
		pgVersion = "15"
	}

	// 1. Generate Credentials
	dbUser := "user_" + randomString(5)
	dbPass := "pass_" + randomString(8)
	cleanJobID := strings.ReplaceAll(jobID, "-", "")
	if len(cleanJobID) > 8 {
		cleanJobID = cleanJobID[:8]
	}
	dbName := "db_" + cleanJobID

	hostPort, err := getFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to find free port: %v", err)
	}

	// 2. Pull Image (Dynamic Version)
	// Gunakan alpine agar ringan
	imageName := fmt.Sprintf("postgres:%s-alpine", pgVersion)

	// Cek apakah image ada, kalau tidak ada PULL dulu
	_, _, err = cli.ImageInspectWithRaw(ctx, imageName)
	if client.IsErrNotFound(err) {
		fmt.Printf("Pulling image %s...\n", imageName)
		reader, err := cli.ImagePull(ctx, imageName, types.ImagePullOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to pull image %s: %v", imageName, err)
		}
		io.Copy(io.Discard, reader) // Tunggu sampai selesai pull
		reader.Close()
	} else if err != nil {
		return nil, fmt.Errorf("failed to inspect image: %v", err)
	}

	// 3. Create Container
	containerConfig := &container.Config{
		Image: imageName,
		Env: []string{
			"POSTGRES_USER=" + dbUser,
			"POSTGRES_PASSWORD=" + dbPass,
			"POSTGRES_DB=" + dbName,
		},
	}

	hostConfig := &container.HostConfig{
		PortBindings: nat.PortMap{
			"5432/tcp": []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: fmt.Sprintf("%d", hostPort),
				},
			},
		},
		AutoRemove: false,
	}

	resp, err := cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "restore_job_"+jobID)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %v", err)
	}

	// 4. Start Container
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("failed to start container: %v", err)
	}

	return &EphemeralDB{
		ContainerID: resp.ID,
		Port:        hostPort,
		User:        dbUser,
		Password:    dbPass,
		DBName:      dbName,
		Version:     pgVersion,
	}, nil
}

func (s *DockerService) StopContainer(containerID string) error {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}

	timeout := 1
	return cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
}
