// internal/services/xauth/handlers.go
package xauth

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"platform/internal/platform/natsclient"
	"platform/utils"

	"github.com/nats-io/nats.go"
)

// Handlers — набор NATS-обработчиков сервиса xauth.
type Handlers struct {
	nc  *natsclient.PlatformClient
	cfg Config
}

// NewHandlers создаёт экземпляр Handlers.
func NewHandlers(nc *natsclient.PlatformClient, cfg Config) *Handlers {
	return &Handlers{nc: nc, cfg: cfg}
}

// HandleLogin проверяет логин/пароль и выдаёт пару JWT-токенов в HttpOnly-куках.
//
// Subject: api.v1.xauth.login
// Тело: {"username": "...", "password": "..."}
func (h *Handlers) HandleLogin(msg *nats.Msg) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		utils.ReplyError(msg, 400, "invalid json")
		return
	}

	// Сравниваем в константное время — защита от timing-атаки.
	userOK := hmac.Equal([]byte(req.Username), []byte(h.cfg.Username))
	passOK := hmac.Equal([]byte(req.Password), []byte(h.cfg.Password))
	if !userOK || !passOK {
		utils.ReplyError(msg, 401, "invalid credentials")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	accessCookie, refreshCookie, err := h.issueTokenCookies(ctx)
	if err != nil {
		log.Printf("xauth: HandleLogin: %v", err)
		utils.ReplyError(msg, 500, "failed to issue tokens")
		return
	}

	log.Printf("xauth: login ok, user=%s", h.cfg.Username)
	utils.Reply(msg, 200,
		map[string]string{"status": "ok"},
		"Set-Cookie", accessCookie,
		"Set-Cookie", refreshCookie,
	)
}

// HandleRefresh обновляет access-токен по валидному refresh-токену.
// Refresh-токен ротируется: старый отзывается, выдаётся новый.
//
// Subject: api.v1.xauth.refresh
func (h *Handlers) HandleRefresh(msg *nats.Msg) {
	rawRefresh := utils.GetCookie(msg, "refresh_token")
	if rawRefresh == "" {
		utils.ReplyError(msg, 401, "refresh token missing")
		return
	}

	c, err := VerifyJWT(rawRefresh, h.cfg.RefreshSecret)
	if err != nil {
		utils.ReplyError(msg, 401, "invalid refresh token")
		return
	}
	if time.Now().Unix() > c.Exp {
		utils.ReplyError(msg, 401, "refresh token expired")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Проверяем, что JTI не был отозван (logout или предыдущая ротация).
	val, err := h.nc.GetValue(ctx, h.cfg.NATS.KV.BucketName, c.Jti)
	if err != nil || val == nil || string(val) == "revoked" {
		utils.ReplyError(msg, 401, "refresh token revoked")
		return
	}

	// Отзываем старый JTI до выдачи нового — ротация безопасна на уровне KV.
	if err := h.nc.PutValue(ctx, h.cfg.NATS.KV.BucketName, c.Jti, []byte("revoked")); err != nil {
		utils.ReplyError(msg, 500, "failed to revoke old token")
		return
	}

	accessCookie, refreshCookie, err := h.issueTokenCookies(ctx)
	if err != nil {
		log.Printf("xauth: HandleRefresh: %v", err)
		utils.ReplyError(msg, 500, "failed to issue tokens")
		return
	}

	log.Printf("xauth: refresh ok, user=%s", c.Sub)
	utils.Reply(msg, 200,
		map[string]string{"status": "ok"},
		"Set-Cookie", accessCookie,
		"Set-Cookie", refreshCookie,
	)
}

// HandleLogout отзывает refresh-токен в KV и выставляет куки с MaxAge=-1 (удаление).
//
// Subject: api.v1.xauth.logout
func (h *Handlers) HandleLogout(msg *nats.Msg) {
	if rawRefresh := utils.GetCookie(msg, "refresh_token"); rawRefresh != "" {
		if c, err := VerifyJWT(rawRefresh, h.cfg.RefreshSecret); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			// Отзываем JTI — даже если токен ещё не истёк, /refresh его не примет.
			_ = h.nc.PutValue(ctx, h.cfg.NATS.KV.BucketName, c.Jti, []byte("revoked"))
		}
	}

	clearAccess := utils.BuildSetCookie("access_token", "", h.cfg.CookieDomain, -1, h.cfg.CookieSecure)
	clearRefresh := utils.BuildSetCookie("refresh_token", "", h.cfg.CookieDomain, -1, h.cfg.CookieSecure)

	log.Println("xauth: logout")
	utils.Reply(msg, 200,
		map[string]string{"status": "ok"},
		"Set-Cookie", clearAccess,
		"Set-Cookie", clearRefresh,
	)
}

// HandleMe валидирует access-токен из куки и возвращает claims текущего пользователя.
// Используется клиентом для проверки сессии без хранения токена в JS.
//
// Subject: api.v1.xauth.me
func (h *Handlers) HandleMe(msg *nats.Msg) {
	rawAccess := utils.GetCookie(msg, "access_token")
	if rawAccess == "" {
		utils.ReplyError(msg, 401, "access token missing")
		return
	}

	c, err := VerifyJWT(rawAccess, h.cfg.AccessSecret)
	if err != nil {
		utils.ReplyError(msg, 401, "invalid access token")
		return
	}
	if time.Now().Unix() > c.Exp {
		utils.ReplyError(msg, 401, "access token expired")
		return
	}

	utils.Reply(msg, 200, map[string]any{
		"sub": c.Sub,
		"exp": c.Exp,
		"iat": c.Iat,
	})
}

// =============================================================================
// Вспомогательные методы
// =============================================================================

// issueTokenCookies выдаёт новую пару JWT-токенов и возвращает готовые строки
// Set-Cookie для передачи через gateway браузеру.
// JTI refresh-токена сохраняется в KV для последующего отзыва.
func (h *Handlers) issueTokenCookies(ctx context.Context) (accessCookie, refreshCookie string, err error) {
	now := time.Now()

	accessJTI, err := NewJTI()
	if err != nil {
		return "", "", fmt.Errorf("jti(access): %w", err)
	}
	refreshJTI, err := NewJTI()
	if err != nil {
		return "", "", fmt.Errorf("jti(refresh): %w", err)
	}

	access, err := SignJWT(Claims{
		Sub: h.cfg.Username,
		Exp: now.Add(h.cfg.AccessTTL).Unix(),
		Jti: accessJTI,
		Iat: now.Unix(),
	}, h.cfg.AccessSecret)
	if err != nil {
		return "", "", fmt.Errorf("sign access: %w", err)
	}

	refresh, err := SignJWT(Claims{
		Sub: h.cfg.Username,
		Exp: now.Add(h.cfg.RefreshTTL).Unix(),
		Jti: refreshJTI,
		Iat: now.Unix(),
	}, h.cfg.RefreshSecret)
	if err != nil {
		return "", "", fmt.Errorf("sign refresh: %w", err)
	}

	// Сохраняем JTI refresh-токена в KV.
	// При logout/ротации значение перезаписывается строкой "revoked".
	expBytes := []byte(fmt.Sprintf("%d", now.Add(h.cfg.RefreshTTL).Unix()))
	if err := h.nc.PutValue(ctx, h.cfg.NATS.KV.BucketName, refreshJTI, expBytes); err != nil {
		return "", "", fmt.Errorf("kv put refresh jti: %w", err)
	}

	accessCookie = utils.BuildSetCookie(
		"access_token", access, h.cfg.CookieDomain,
		int(h.cfg.AccessTTL.Seconds()), h.cfg.CookieSecure,
	)
	refreshCookie = utils.BuildSetCookie(
		"refresh_token", refresh, h.cfg.CookieDomain,
		int(h.cfg.RefreshTTL.Seconds()), h.cfg.CookieSecure,
	)

	return accessCookie, refreshCookie, nil
}
