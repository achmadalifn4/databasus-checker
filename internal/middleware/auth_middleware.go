package middleware

import (
	"databasus-checker/internal/services"
	"databasus-checker/internal/utils"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

func Setup(e *echo.Echo) {
	authService := services.AuthService{}

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			path := c.Path()

			// Bypass assets
			if strings.HasPrefix(path, "/assets") {
				return next(c)
			}

			// 1. Cek Fresh Install (Prioritas Utama)
			if authService.IsFreshInstall() {
				if path != "/install" && path != "/api/install" {
					return c.Redirect(http.StatusFound, "/install")
				}
				return next(c)
			}

			// Blokir akses install jika sudah installed
			if path == "/install" || path == "/api/install" {
				return c.Redirect(http.StatusFound, "/login")
			}

			// 2. Proteksi Halaman Dashboard & API (Route Guard)
			// Daftar route yang boleh diakses TANPA login
			publicRoutes := map[string]bool{
				"/login":     true,
				"/api/login": true,
			}

			// Jika route TIDAK public, maka WAJIB cek Token
			if !publicRoutes[path] {
				cookie, err := c.Cookie("auth_token")
				if err != nil {
					return c.Redirect(http.StatusFound, "/login")
				}

				claims, err := utils.ValidateToken(cookie.Value)
				if err != nil {
					return c.Redirect(http.StatusFound, "/login")
				}

				// Simpan user info di context (siapa tau nanti butuh)
				c.Set("user_id", claims.UserID)
				c.Set("role", claims.Role)
			}

			// Jika user SUDAH login tapi buka halaman login, lempar ke dashboard
			if path == "/login" {
				if _, err := c.Cookie("auth_token"); err == nil {
					return c.Redirect(http.StatusFound, "/")
				}
			}

			return next(c)
		}
	})
}
