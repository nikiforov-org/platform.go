// internal/services/xhttp/handlers.go
package xhttp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"platform/internal/platform/natsclient"
	"platform/utils"

	"github.com/nats-io/nats.go"
)

// Item — сущность предметной области сервиса xhttp.
type Item struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Handlers — набор NATS-обработчиков сервиса.
// Хранит все зависимости, необходимые для выполнения запросов.
type Handlers struct {
	nc    *natsclient.PlatformClient
	db    *sql.DB
	cache *cache
}

// NewHandlers создаёт экземпляр Handlers с переданными зависимостями.
func NewHandlers(nc *natsclient.PlatformClient, db *sql.DB, cfg Config) *Handlers {
	return &Handlers{
		nc:    nc,
		db:    db,
		cache: newCache(nc, cfg.NATS.KV.BucketName),
	}
}

// HandleCreate создаёт новую запись в PostgreSQL и инвалидирует кэш списка.
//
// Subject: api.v1.xhttp.create
// Тело: {"name": "...", "value": "..."}
func (h *Handlers) HandleCreate(msg *nats.Msg) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		utils.ReplyError(msg, 400, "invalid json")
		return
	}

	var it Item
	err := h.db.QueryRowContext(ctx, `
		INSERT INTO items (name, value)
		VALUES ($1, $2)
		RETURNING id, name, value, created_at, updated_at`,
		req.Name, req.Value,
	).Scan(&it.ID, &it.Name, &it.Value, &it.CreatedAt, &it.UpdatedAt)
	if err != nil {
		utils.ReplyError(msg, 500, "db error")
		return
	}

	h.cache.Invalidate(ctx, "list")
	utils.Reply(msg, 201, it)
}

// HandleGet возвращает запись по ID. Результат кэшируется в NATS KV.
//
// Subject: api.v1.xhttp.get
// Тело: {"id": 1}
func (h *Handlers) HandleGet(msg *nats.Msg) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		utils.ReplyError(msg, 400, "invalid json")
		return
	}

	cacheKey := fmt.Sprintf("item:%d", req.ID)

	// Cache hit.
	if cached := h.cache.Get(ctx, cacheKey); len(cached) > 0 {
		var it Item
		if json.Unmarshal(cached, &it) == nil {
			utils.Reply(msg, 200, it)
			return
		}
	}

	// Cache miss — запрос в PostgreSQL.
	var it Item
	err := h.db.QueryRowContext(ctx, `
		SELECT id, name, value, created_at, updated_at
		FROM items WHERE id = $1`,
		req.ID,
	).Scan(&it.ID, &it.Name, &it.Value, &it.CreatedAt, &it.UpdatedAt)
	if err == sql.ErrNoRows {
		utils.ReplyError(msg, 404, "not found")
		return
	}
	if err != nil {
		utils.ReplyError(msg, 500, "db error")
		return
	}

	if encoded, err := json.Marshal(it); err == nil {
		h.cache.Put(ctx, cacheKey, encoded)
	}

	utils.Reply(msg, 200, it)
}

// HandleList возвращает список всех записей. Результат кэшируется.
//
// Subject: api.v1.xhttp.list
func (h *Handlers) HandleList(msg *nats.Msg) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Cache hit.
	if cached := h.cache.Get(ctx, "list"); len(cached) > 0 {
		var items []Item
		if json.Unmarshal(cached, &items) == nil {
			utils.Reply(msg, 200, items)
			return
		}
	}

	// Cache miss.
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, name, value, created_at, updated_at
		FROM items ORDER BY id`)
	if err != nil {
		utils.ReplyError(msg, 500, "db error")
		return
	}
	defer rows.Close()

	items := make([]Item, 0)
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.Name, &it.Value, &it.CreatedAt, &it.UpdatedAt); err != nil {
			utils.ReplyError(msg, 500, "db scan error")
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		utils.ReplyError(msg, 500, "db error")
		return
	}

	if encoded, err := json.Marshal(items); err == nil {
		h.cache.Put(ctx, "list", encoded)
	}

	utils.Reply(msg, 200, items)
}

// HandleUpdate обновляет запись и инвалидирует кэш записи и списка.
//
// Subject: api.v1.xhttp.update
// Тело: {"id": 1, "name": "...", "value": "..."}
func (h *Handlers) HandleUpdate(msg *nats.Msg) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var req struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		utils.ReplyError(msg, 400, "invalid json")
		return
	}

	var it Item
	err := h.db.QueryRowContext(ctx, `
		UPDATE items SET name = $1, value = $2, updated_at = NOW()
		WHERE id = $3
		RETURNING id, name, value, created_at, updated_at`,
		req.Name, req.Value, req.ID,
	).Scan(&it.ID, &it.Name, &it.Value, &it.CreatedAt, &it.UpdatedAt)
	if err == sql.ErrNoRows {
		utils.ReplyError(msg, 404, "not found")
		return
	}
	if err != nil {
		utils.ReplyError(msg, 500, "db error")
		return
	}

	h.cache.Invalidate(ctx, fmt.Sprintf("item:%d", req.ID), "list")
	utils.Reply(msg, 200, it)
}

// HandleDelete удаляет запись и инвалидирует кэш.
//
// Subject: api.v1.xhttp.delete
// Тело: {"id": 1}
func (h *Handlers) HandleDelete(msg *nats.Msg) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		utils.ReplyError(msg, 400, "invalid json")
		return
	}

	res, err := h.db.ExecContext(ctx, `DELETE FROM items WHERE id = $1`, req.ID)
	if err != nil {
		utils.ReplyError(msg, 500, "db error")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		utils.ReplyError(msg, 404, "not found")
		return
	}

	h.cache.Invalidate(ctx, fmt.Sprintf("item:%d", req.ID), "list")
	utils.Reply(msg, 204, nil)
}
