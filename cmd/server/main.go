package main

import (
	"databasus-checker/internal/database"
	"databasus-checker/internal/middleware"
	"databasus-checker/internal/models"
	"databasus-checker/internal/services"
	"databasus-checker/internal/utils"
	"databasus-checker/internal/worker"
	"flag"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

type TemplateRenderer struct {
	templatesDir string
}

func (t *TemplateRenderer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	layout := filepath.Join(t.templatesDir, "layout.html")
	view := filepath.Join(t.templatesDir, name)
	tmpl, err := template.ParseFiles(layout, view)
	if err != nil {
		return err
	}
	return tmpl.ExecuteTemplate(w, "base", data)
}

func (t *TemplateRenderer) RenderDashboard(w io.Writer, name string, data interface{}, activeMenu string) error {
	layout := filepath.Join(t.templatesDir, "dashboard_layout.html")
	view := filepath.Join(t.templatesDir, name)

	tmpl, err := template.ParseFiles(layout, view)
	if err != nil {
		return err
	}

	dataMap := make(map[string]interface{})
	if data != nil {
		if m, ok := data.(echo.Map); ok {
			for k, v := range m {
				dataMap[k] = v
			}
		} else if m, ok := data.(map[string]interface{}); ok {
			for k, v := range m {
				dataMap[k] = v
			}
		}
	}
	dataMap["activeMenu"] = activeMenu
	return tmpl.ExecuteTemplate(w, "dashboard_layout", dataMap)
}

func main() {
	_ = godotenv.Load()

	newPass := flag.String("new-password", "", "Set new password")
	targetEmail := flag.String("email", "", "Email target")
	flag.Parse()

	database.Connect()

	if *newPass != "" && *targetEmail != "" {
		handlePasswordReset(*targetEmail, *newPass)
		return
	}

	// Start Worker
	bgWorker := worker.NewWorker()
	bgWorker.Start()

	e := echo.New()
	e.Renderer = &TemplateRenderer{templatesDir: "web/templates"}
	middleware.Setup(e)

	// --- PUBLIC ROUTES ---
	e.GET("/install", func(c echo.Context) error { return c.Render(http.StatusOK, "install.html", nil) })
	e.GET("/login", func(c echo.Context) error { return c.Render(http.StatusOK, "login.html", nil) })

	authService := services.AuthService{}

	e.POST("/api/install", func(c echo.Context) error {
		email := c.FormValue("email")
		password := c.FormValue("password")
		if err := authService.CreateFirstAdmin(email, password); err != nil {
			return c.String(http.StatusBadRequest, err.Error())
		}
		return c.Redirect(http.StatusFound, "/login")
	})

	e.POST("/api/login", func(c echo.Context) error {
		email := c.FormValue("email")
		password := c.FormValue("password")
		user, err := authService.Authenticate(email, password)
		if err != nil {
			return c.String(http.StatusUnauthorized, "Invalid Credentials")
		}
		token, _ := utils.GenerateToken(user.ID.String(), user.Role)
		cookie := new(http.Cookie)
		cookie.Name = "auth_token"
		cookie.Value = token
		cookie.Expires = time.Now().Add(24 * time.Hour)
		cookie.HttpOnly = true
		cookie.Path = "/"
		c.SetCookie(cookie)
		return c.Redirect(http.StatusFound, "/")
	})

	e.GET("/logout", func(c echo.Context) error {
		cookie := new(http.Cookie)
		cookie.Name = "auth_token"
		cookie.Value = ""
		cookie.Expires = time.Now().Add(-1 * time.Hour)
		cookie.Path = "/"
		c.SetCookie(cookie)
		return c.Redirect(http.StatusFound, "/login")
	})

	// ==========================================
	// --- DASHBOARD & SERVICES INIT ---
	// ==========================================

	databasusClient := services.DatabasusClient{}
	queueService := services.QueueService{}

	// Route Dashboard (Menampilkan History Log)
	e.GET("/", func(c echo.Context) error {
		history, _ := queueService.GetJobHistory(20) // Get last 20 logs
		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "dashboard_index.html", echo.Map{
			"History": history,
		}, "dashboard")
	})

	e.GET("/settings", func(c echo.Context) error {
		settings := models.GetSettings(database.DB)
		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "settings.html", echo.Map{"Settings": settings}, "settings")
	})

	e.POST("/api/settings", func(c echo.Context) error {
		settings := models.GetSettings(database.DB)
		settings.DatabasusURL = c.FormValue("databasus_url")
		settings.DatabasusUser = c.FormValue("databasus_user")
		settings.DatabasusPassword = c.FormValue("databasus_password")
		settings.AppTimezone = c.FormValue("app_timezone")
		settings.DatabasusTimezone = c.FormValue("databasus_timezone")
		retentionDays, _ := strconv.Atoi(c.FormValue("log_retention_days"))
		if retentionDays > 0 {
			settings.LogRetentionDays = retentionDays
		}
		database.DB.Save(&settings)
		return c.Redirect(http.StatusFound, "/settings")
	})

	// ==========================================
	// --- RESTORE TESTS (CRUD) ---
	// ==========================================

	e.GET("/tests", func(c echo.Context) error {
		var tests []models.RestoreTestConfig
		database.DB.Order("created_at desc").Find(&tests)
		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "tests_list.html", echo.Map{"Tests": tests}, "tests")
	})

	e.GET("/tests/create", func(c echo.Context) error {
		workspaces, err := databasusClient.GetWorkspaces()
		if err != nil {
			return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "tests_form.html", echo.Map{"Error": "Could not fetch workspaces. Check settings."}, "tests")
		}
		var storages []models.StorageConfig
		var notifications []models.NotificationConfig
		database.DB.Find(&storages)
		database.DB.Find(&notifications)

		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "tests_form.html", echo.Map{
			"Workspaces":    workspaces,
			"Storages":      storages,
			"Notifications": notifications,
		}, "tests")
	})

	e.GET("/api/proxy/databases", func(c echo.Context) error {
		workspaceID := c.QueryParam("workspace_id")
		dbs, err := databasusClient.GetDatabases(workspaceID)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, dbs)
	})

	// --- TRIGGER MANUAL RUN ---
	e.POST("/api/tests/:id/run", func(c echo.Context) error {
		idParam := c.Param("id")
		// ID sudah UUID, jangan convert ke int
		_, err := queueService.Enqueue(idParam)
		if err != nil {
			return c.Redirect(http.StatusFound, "/tests?error="+err.Error())
		}
		return c.Redirect(http.StatusFound, "/tests?success=Test+queued+successfully")
	})

	e.POST("/api/tests", func(c echo.Context) error {
		c.Request().ParseForm()
		storageIDs := c.Request().Form["storage_ids"]
		notificationIDs := c.Request().Form["notification_ids"]

		config := models.RestoreTestConfig{
			Name:                  c.FormValue("name"),
			WorkspaceID:           c.FormValue("workspace_id"),
			DatabasusDatabaseID:   c.FormValue("database_id"),
			DatabasusDatabaseName: c.FormValue("database_name"),
			PreRestoreScript:      c.FormValue("pre_restore_script"),
			PostRestoreScript:     c.FormValue("post_restore_script"),
			StorageIDs:            storageIDs,
			NotificationIDs:       notificationIDs,
		}

		if err := database.DB.Create(&config).Error; err != nil {
			return c.String(http.StatusBadRequest, "Failed to save: "+err.Error())
		}
		return c.Redirect(http.StatusFound, "/tests")
	})

	// FIXED: Unlink Jobs First, Then Hard Delete
	e.POST("/api/tests/:id/delete", func(c echo.Context) error {
		id := c.Param("id")

		// 1. Unlink Jobs (Set parent_id to NULL to keep history)
		if err := database.DB.Model(&models.Job{}).
			Where("restore_test_config_id = ?", id).
			Update("restore_test_config_id", nil).Error; err != nil {
			return c.String(http.StatusInternalServerError, "Failed to unlink jobs: "+err.Error())
		}

		// 2. Delete the Test Config (Hard Delete)
		if err := database.DB.Unscoped().Delete(&models.RestoreTestConfig{}, "id = ?", id).Error; err != nil {
			return c.String(http.StatusInternalServerError, "Failed to delete test config: "+err.Error())
		}

		return c.Redirect(http.StatusFound, "/tests")
	})

	// --- ROUTE EDIT (UPDATED) ---
	e.GET("/tests/:id/edit", func(c echo.Context) error {
		id := c.Param("id")
		
		// 1. Ambil Data Test Config
		var test models.RestoreTestConfig
		if err := database.DB.First(&test, "id = ?", id).Error; err != nil {
			return c.Redirect(http.StatusFound, "/tests")
		}

		// 2. Ambil Data Storage & Notification (Untuk Checkbox List)
		var storages []models.StorageConfig
		var notifications []models.NotificationConfig
		database.DB.Order("name asc").Find(&storages)
		database.DB.Order("name asc").Find(&notifications)

		// 3. Render tanpa GetWorkspaces (Read-Only)
		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "tests_edit.html", echo.Map{
			"Test":          test,
			"Storages":      storages,
			"Notifications": notifications,
		}, "tests")
	})

	e.POST("/api/tests/:id/update", func(c echo.Context) error {
		id := c.Param("id")
		var test models.RestoreTestConfig
		if err := database.DB.First(&test, "id = ?", id).Error; err != nil {
			return c.String(http.StatusNotFound, "Test config not found")
		}

		c.Request().ParseForm()
		storageIDs := c.Request().Form["storage_ids"]
		notificationIDs := c.Request().Form["notification_ids"]

		test.Name = c.FormValue("name")
		
		// Logic update Hidden Fields (Database & Workspace)
		// Jika kosong (karena input disabled), biarkan nilai lama. Jika ada, update.
		if wsID := c.FormValue("workspace_id"); wsID != "" {
			test.WorkspaceID = wsID
		}
		if dbID := c.FormValue("database_id"); dbID != "" {
			test.DatabasusDatabaseID = dbID
		}
		if dbName := c.FormValue("database_name"); dbName != "" {
			test.DatabasusDatabaseName = dbName
		}

		test.PreRestoreScript = c.FormValue("pre_restore_script")
		test.PostRestoreScript = c.FormValue("post_restore_script")
		test.StorageIDs = storageIDs
		test.NotificationIDs = notificationIDs

		database.DB.Save(&test)
		return c.Redirect(http.StatusFound, "/tests")
	})

	// ==========================================
	// --- STORAGE MENU (CRUD + Edit) ---
	// ==========================================

	e.GET("/storage", func(c echo.Context) error {
		var storages []models.StorageConfig
		database.DB.Order("created_at desc").Find(&storages)
		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "storage_list.html", echo.Map{"Storages": storages}, "storage")
	})

	e.GET("/storage/create", func(c echo.Context) error {
		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "storage_form.html", nil, "storage")
	})

	parseStorageConfig := func(c echo.Context) (string, string, map[string]interface{}) {
		storageType := c.FormValue("type")
		name := c.FormValue("name")
		configMap := make(map[string]interface{})
		getBool := func(key string) bool { return c.FormValue(key) == "on" }

		switch storageType {
		case "S3":
			configMap["bucket"] = c.FormValue("s3_bucket")
			configMap["region"] = c.FormValue("s3_region")
			configMap["access_key"] = c.FormValue("s3_access_key")
			configMap["secret_key"] = c.FormValue("s3_secret_key")
			configMap["endpoint"] = c.FormValue("s3_endpoint")
			configMap["prefix"] = c.FormValue("s3_prefix")
			configMap["virtual_host"] = getBool("s3_virtual_host")
			configMap["skip_tls"] = getBool("s3_skip_tls")
		case "NAS":
			configMap["host"] = c.FormValue("nas_host")
			configMap["port"] = c.FormValue("nas_port")
			configMap["share"] = c.FormValue("nas_share")
			configMap["path"] = c.FormValue("nas_path")
			configMap["user"] = c.FormValue("nas_user")
			configMap["password"] = c.FormValue("nas_password")
			configMap["domain"] = c.FormValue("nas_domain")
			configMap["ssl"] = getBool("nas_ssl")
		case "FTP":
			configMap["host"] = c.FormValue("ftp_host")
			configMap["port"] = c.FormValue("ftp_port")
			configMap["user"] = c.FormValue("ftp_user")
			configMap["password"] = c.FormValue("ftp_password")
			configMap["path"] = c.FormValue("ftp_path")
			configMap["ssl"] = getBool("ftp_ssl")
		case "SFTP":
			configMap["host"] = c.FormValue("sftp_host")
			configMap["port"] = c.FormValue("sftp_port")
			configMap["user"] = c.FormValue("sftp_user")
			configMap["path"] = c.FormValue("sftp_path")
			configMap["auth_method"] = c.FormValue("sftp_auth_method")
			configMap["password"] = c.FormValue("sftp_password")
			configMap["private_key"] = c.FormValue("sftp_private_key")
			configMap["skip_host_key"] = getBool("sftp_skip_host_key")
		case "RCLONE":
			configMap["config_content"] = c.FormValue("rclone_config")
			configMap["remote_path"] = c.FormValue("rclone_path")
		}
		return storageType, name, configMap
	}

	e.POST("/api/storage", func(c echo.Context) error {
		storageType, name, configMap := parseStorageConfig(c)
		storage := models.StorageConfig{
			Name:   name,
			Type:   storageType,
			Config: configMap,
		}
		if err := database.DB.Create(&storage).Error; err != nil {
			return c.String(http.StatusBadRequest, "Failed to save storage: "+err.Error())
		}
		return c.Redirect(http.StatusFound, "/storage")
	})

	e.POST("/api/storage/:id/delete", func(c echo.Context) error {
		id := c.Param("id")
		database.DB.Delete(&models.StorageConfig{}, "id = ?", id)
		return c.Redirect(http.StatusFound, "/storage")
	})

	e.GET("/storage/:id/edit", func(c echo.Context) error {
		id := c.Param("id")
		var storage models.StorageConfig
		if err := database.DB.First(&storage, "id = ?", id).Error; err != nil {
			return c.Redirect(http.StatusFound, "/storage")
		}
		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "storage_edit.html", echo.Map{"Storage": storage}, "storage")
	})

	e.POST("/api/storage/:id/update", func(c echo.Context) error {
		id := c.Param("id")
		var storage models.StorageConfig
		if err := database.DB.First(&storage, "id = ?", id).Error; err != nil {
			return c.String(http.StatusNotFound, "Storage not found")
		}
		storageType, name, configMap := parseStorageConfig(c)
		storage.Name = name
		storage.Type = storageType
		storage.Config = configMap
		database.DB.Save(&storage)
		return c.Redirect(http.StatusFound, "/storage")
	})

	e.POST("/api/storage/test-connection", func(c echo.Context) error {
		workspaces, err := databasusClient.GetWorkspaces()
		if err != nil || len(workspaces) == 0 {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"message": "Failed to test connection: No active Workspace found. Check Settings.",
			})
		}
		activeWorkspaceID := workspaces[0].ID

		storageType, name, _ := parseStorageConfig(c)
		payload := make(map[string]interface{})
		payload["workspaceId"] = activeWorkspaceID
		payload["type"] = storageType
		payload["name"] = name + " (Test)"

		getBool := func(key string) bool { return c.FormValue(key) == "on" }

		switch storageType {
		case "S3":
			payload["s3Storage"] = map[string]interface{}{
				"s3Bucket":                c.FormValue("s3_bucket"),
				"s3Region":                c.FormValue("s3_region"),
				"s3AccessKey":             c.FormValue("s3_access_key"),
				"s3SecretKey":             c.FormValue("s3_secret_key"),
				"s3Endpoint":              c.FormValue("s3_endpoint"),
				"s3Prefix":                c.FormValue("s3_prefix"),
				"s3UseVirtualHostedStyle": getBool("s3_virtual_host"),
				"skipTLSVerify":           getBool("s3_skip_tls"),
			}
		case "NAS":
			port, _ := strconv.Atoi(c.FormValue("nas_port"))
			payload["nasStorage"] = map[string]interface{}{
				"host":     c.FormValue("nas_host"),
				"port":     port,
				"share":    c.FormValue("nas_share"),
				"path":     c.FormValue("nas_path"),
				"username": c.FormValue("nas_user"),
				"password": c.FormValue("nas_password"),
				"domain":   c.FormValue("nas_domain"),
				"useSsl":   getBool("nas_ssl"),
			}
		case "FTP":
			port, _ := strconv.Atoi(c.FormValue("ftp_port"))
			payload["ftpStorage"] = map[string]interface{}{
				"host":     c.FormValue("ftp_host"),
				"port":     port,
				"username": c.FormValue("ftp_user"),
				"password": c.FormValue("ftp_password"),
				"path":     c.FormValue("ftp_path"),
				"useSsl":   getBool("ftp_ssl"),
			}
		case "SFTP":
			port, _ := strconv.Atoi(c.FormValue("sftp_port"))
			payload["sftpStorage"] = map[string]interface{}{
				"host":              c.FormValue("sftp_host"),
				"port":              port,
				"username":          c.FormValue("sftp_user"),
				"path":              c.FormValue("sftp_path"),
				"password":          c.FormValue("sftp_password"),
				"privateKey":        c.FormValue("sftp_private_key"),
				"skipHostKeyVerify": getBool("sftp_skip_host_key"),
			}
		case "RCLONE":
			payload["rcloneStorage"] = map[string]interface{}{
				"configContent": c.FormValue("rclone_config"),
				"remotePath":    c.FormValue("rclone_path"),
			}
		}

		if err := databasusClient.TestStorageConnection(payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"message": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]string{"message": "Connection successful!"})
	})

	// ==========================================
	// --- NOTIFICATIONS MENU (CRUD + Edit) ---
	// ==========================================

	e.GET("/notifications", func(c echo.Context) error {
		var notifs []models.NotificationConfig
		database.DB.Order("created_at desc").Find(&notifs)
		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "notification_list.html", echo.Map{"Notifications": notifs}, "notifications")
	})

	e.GET("/notifications/create", func(c echo.Context) error {
		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "notification_form.html", nil, "notifications")
	})

	parseNotifConfig := func(c echo.Context) (string, string, map[string]interface{}) {
		notifType := c.FormValue("type")
		name := c.FormValue("name")
		configMap := make(map[string]interface{})

		switch notifType {
		case "TELEGRAM":
			configMap["bot_token"] = c.FormValue("telegram_token")
			configMap["chat_id"] = c.FormValue("telegram_chat_id")
		case "EMAIL":
			configMap["host"] = c.FormValue("email_host")
			configMap["port"] = c.FormValue("email_port")
			configMap["user"] = c.FormValue("email_user")
			configMap["password"] = c.FormValue("email_password")
			configMap["from_email"] = c.FormValue("email_from")
			configMap["to_email"] = c.FormValue("email_to")
		}
		return notifType, name, configMap
	}

	e.POST("/api/notifications", func(c echo.Context) error {
		notifType, name, configMap := parseNotifConfig(c)
		notif := models.NotificationConfig{
			Name:   name,
			Type:   notifType,
			Config: configMap,
		}
		if err := database.DB.Create(&notif).Error; err != nil {
			return c.String(http.StatusBadRequest, "Failed to save notification: "+err.Error())
		}
		return c.Redirect(http.StatusFound, "/notifications")
	})

	e.POST("/api/notifications/:id/delete", func(c echo.Context) error {
		id := c.Param("id")
		database.DB.Delete(&models.NotificationConfig{}, "id = ?", id)
		return c.Redirect(http.StatusFound, "/notifications")
	})

	e.GET("/notifications/:id/edit", func(c echo.Context) error {
		id := c.Param("id")
		var notif models.NotificationConfig
		if err := database.DB.First(&notif, "id = ?", id).Error; err != nil {
			return c.Redirect(http.StatusFound, "/notifications")
		}
		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "notification_edit.html", echo.Map{"Notification": notif}, "notifications")
	})

	e.POST("/api/notifications/:id/update", func(c echo.Context) error {
		id := c.Param("id")
		var notif models.NotificationConfig
		if err := database.DB.First(&notif, "id = ?", id).Error; err != nil {
			return c.String(http.StatusNotFound, "Notification not found")
		}
		notifType, name, configMap := parseNotifConfig(c)
		notif.Name = name
		notif.Type = notifType
		notif.Config = configMap
		database.DB.Save(&notif)
		return c.Redirect(http.StatusFound, "/notifications")
	})

	e.POST("/api/notifications/test-connection", func(c echo.Context) error {
		notifType, _, configMap := parseNotifConfig(c)

		if notifType == "TELEGRAM" {
			token := configMap["bot_token"].(string)
			chatId := configMap["chat_id"].(string)
			if err := utils.SendTelegram(token, chatId, "Databasus Checker: This is a test notification."); err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"message": "Telegram Error: " + err.Error()})
			}
		} else if notifType == "EMAIL" {
			host := configMap["host"].(string)
			portStr := configMap["port"].(string)
			port, _ := strconv.Atoi(portStr)
			user := configMap["user"].(string)
			pass := configMap["password"].(string)
			from := configMap["from_email"].(string)
			to := configMap["to_email"].(string)
			if err := utils.SendEmail(host, port, user, pass, from, to, "Databasus Checker Test", "This is a test notification from Databasus Checker."); err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"message": "Email Error: " + err.Error()})
			}
		} else {
			return c.JSON(http.StatusBadRequest, map[string]string{"message": "Unsupported notification type"})
		}
		return c.JSON(http.StatusOK, map[string]string{"message": "Test notification sent successfully!"})
	})

	// --- QUEUE ROUTE (Queue Only) ---
	e.GET("/queue", func(c echo.Context) error {
		jobs, err := queueService.GetActiveJobs()
		if err != nil {
			return c.String(http.StatusInternalServerError, err.Error())
		}
		return e.Renderer.(*TemplateRenderer).RenderDashboard(c.Response().Writer, "queue_list.html", echo.Map{"Jobs": jobs}, "queue")
	})

	serverPort := os.Getenv("APP_PORT")
	if serverPort == "" {
		serverPort = "4006"
	}
	e.Logger.Fatal(e.Start(":" + serverPort))
}

func handlePasswordReset(email, password string) {
	var user models.User
	result := database.DB.Where("email = ?", email).First(&user)
	hashed, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)

	if result.Error != nil {
		log.Printf("User %s not found. Creating new admin user...", email)
		user = models.User{Email: email, Password: string(hashed), Role: "ADMIN"}
		if err := database.DB.Create(&user).Error; err != nil {
			log.Fatal("Failed to create user: ", err)
		}
	} else {
		user.Password = string(hashed)
		if err := database.DB.Save(&user).Error; err != nil {
			log.Fatal("Failed to update password: ", err)
		}
	}
	os.Exit(0)
}
