package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"time"
)

// --- Telegram Logic ---

type TelegramPayload struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

func SendTelegram(token, chatID, message string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := TelegramPayload{
		ChatID: chatID,
		Text:   message,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(apiURL, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("telegram api error: status code %d", resp.StatusCode)
	}

	return nil
}

// --- Email Logic ---

func SendEmail(host string, port int, user, password, from, to, subject, body string) error {
	auth := smtp.PlainAuth("", user, password, host)
	addr := fmt.Sprintf("%s:%d", host, port)

	msg := []byte("To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=\"utf-8\"\r\n" +
		"\r\n" +
		body + "\r\n")

	// Jika port 465 (SSL/Implicit TLS) biasanya butuh penanganan khusus,
	// tapi untuk starttls standar (587) kode ini cukup.
	// Untuk kestabilan di tool sederhana, kita pakai standard library net/smtp.
	err := smtp.SendMail(addr, auth, from, []string{to}, msg)
	if err != nil {
		return err
	}

	return nil
}
