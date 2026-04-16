// internal/platform/nc/client.go
//
// nc (NATS Client) — обёртка над подключением к NATS.
// Предоставляет единую точку инициализации соединения, JetStream и KV-хранилища.
package nc

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
)

// PlatformClient — обёртка над подключением к NATS.
// Предоставляет доступ к сырому соединению (Conn) и JetStream API (JS).
type PlatformClient struct {
	Conn *nats.Conn
	JS   jetstream.JetStream
	log  zerolog.Logger
}

// Config — полная конфигурация подключения к NATS.
//
// Все поля читаются из файла конфигурации приложения (например, app.yaml).
// Прямое обращение к переменным окружения внутри этого пакета запрещено —
// переменные окружения ($NATS_CLUSTER_USER, $NATS_CLUSTER_PASSWORD и др.)
// раскрываются на уровне systemd-юнита и попадают сюда уже как обычные строки
// через Config.Auth.User / Config.Auth.Password.
//
// Соответствие полям deployments/nats/nats.conf:
//
//	Server.Host                 → "127.0.0.1" (микросервисы всегда подключаются локально)
//	Server.ClientPort           → port: 4222
//	Auth.User                   → authorization { user: $NATS_CLUSTER_USER }
//	Auth.Password               → authorization { password: $NATS_CLUSTER_PASSWORD }
//	KV.BucketName               → имя платформенного KV-бакета
//	KV.Replicas                 → число узлов кластера (cluster { routes })
//	KV.History                  → глубина истории ключей (аудит)
//	KV.MaxValueSize             → согласуется с jetstream { max_mem: 512M }
//	Reconnect.MaxAttempts (-1)  → бесконечный реконнект (production-рекомендация)
//	Reconnect.WaitDuration      → пауза между попытками переподключения
type Config struct {
	// Server — сетевые параметры NATS-сервера.
	Server ServerConfig

	// Auth — учётные данные для блока authorization{} в nats.conf.
	// Если оба поля пусты, авторизация не передаётся (режим локальной разработки).
	Auth AuthConfig

	// KV — параметры платформенного JetStream Key-Value хранилища.
	// Если KV.BucketName пустое, инициализация KV пропускается.
	KV KVConfig

	// Reconnect — поведение при потере связи с NATS.
	Reconnect ReconnectConfig
}

// ServerConfig — сетевые координаты NATS-сервера.
type ServerConfig struct {
	// Host — адрес NATS-сервера.
	// Микросервисы всегда подключаются к локальному агенту (port: 4222 в nats.conf),
	// поэтому в production значение всегда "127.0.0.1".
	Host string

	// ClientPort — клиентский порт NATS (port: 4222 в nats.conf).
	ClientPort int
}

// AuthConfig — учётные данные для блока authorization{} в nats.conf.
type AuthConfig struct {
	// User — логин (authorization.user в nats.conf).
	// Значение поступает раскрытым из конфига приложения,
	// который в systemd-юните получает его из $NATS_CLUSTER_USER.
	User string

	// Password — пароль (authorization.password в nats.conf).
	// Значение поступает раскрытым из конфига приложения,
	// который в systemd-юните получает его из $NATS_CLUSTER_PASSWORD.
	Password string
}

// KVConfig — параметры JetStream Key-Value бакета.
//
// Квоты (MaxValueSize) должны быть согласованы с блоком
// jetstream { max_mem: 512M, max_file: 10G } в nats.conf.
type KVConfig struct {
	// BucketName — имя бакета. Если пустое, KV не инициализируется.
	BucketName string

	// Replicas — число реплик бакета.
	// Должно совпадать с числом маршрутов в cluster { routes } в nats.conf.
	// При трёх узлах кластера — значение 3.
	Replicas int

	// History — число хранимых ревизий одного ключа (диапазон 1–64).
	// Значение 5 покрывает лёгкий аудит без избыточного расхода диска.
	History uint8

	// MaxValueSize — максимальный размер одного значения в байтах.
	// 0 означает ограничение по умолчанию на стороне NATS-сервера.
	// Согласуется с jetstream.max_mem (512M) в nats.conf.
	MaxValueSize int32
}

// ReconnectConfig — поведение клиента при разрыве соединения с NATS.
type ReconnectConfig struct {
	// MaxAttempts — максимальное число попыток переподключения.
	// -1 означает бесконечный реконнект (рекомендуется для production,
	// т.к. Nomad перезапустит процесс при необходимости).
	MaxAttempts int

	// WaitDuration — пауза между попытками переподключения.
	WaitDuration time.Duration
}

// DefaultConfig возвращает Config с разумными значениями по умолчанию,
// полностью соответствующими deployments/nats/nats.conf.
//
// Auth.User и Auth.Password намеренно оставлены пустыми —
// caller обязан заполнить их из конфига приложения перед передачей в NewClient.
func DefaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Host:       "127.0.0.1",
			ClientPort: 4222, // port: 4222 в nats.conf
		},
		Auth: AuthConfig{
			// User и Password не имеют значений по умолчанию —
			// они всегда уникальны для окружения и приходят из конфига приложения.
			User:     "",
			Password: "",
		},
		KV: KVConfig{
			BucketName:   "platform_state",
			Replicas:     0, // 0 — автоопределение по размеру кластера при подключении
			History:      5, // последние 5 ревизий ключа
			MaxValueSize: 0, // без дополнительного ограничения на уровне Go
		},
		Reconnect: ReconnectConfig{
			MaxAttempts:  -1, // бесконечный реконнект
			WaitDuration: 2 * time.Second,
		},
	}
}

// url формирует строку подключения из Server-полей конфига.
func (c Config) url() string {
	return fmt.Sprintf("nats://%s:%d", c.Server.Host, c.Server.ClientPort)
}

// NewClient создаёт подключённый PlatformClient на основе переданного Config.
//
// Порядок инициализации:
//  1. Подключение к NATS с авторизацией и настройками реконнекта.
//  2. Инициализация JetStream.
//  3. Опциональная инициализация KV-бакета (если KV.BucketName не пустой).
//     Если KV.Replicas == 0, число реплик определяется автоматически по размеру кластера.
//
// При любой ошибке после шага 1 соединение закрывается — утечки соединения не будет.
func NewClient(cfg Config, log zerolog.Logger) (*PlatformClient, error) {
	opts := []nats.Option{
		nats.MaxReconnects(cfg.Reconnect.MaxAttempts),
		nats.ReconnectWait(cfg.Reconnect.WaitDuration),

		// Транзиентный разрыв — клиент начинает реконнект автоматически.
		nats.DisconnectErrHandler(func(conn *nats.Conn, err error) {
			log.Warn().Err(err).Msg("NATS: соединение разорвано, переподключение...")
		}),
		nats.ReconnectHandler(func(conn *nats.Conn) {
			log.Info().Str("url", conn.ConnectedUrl()).Msg("NATS: переподключено")
		}),
		// ClosedHandler срабатывает после исчерпания всех попыток реконнекта.
		// При MaxAttempts=-1 вызывается только при явном Close().
		nats.ClosedHandler(func(conn *nats.Conn) {
			log.Info().Msg("NATS: соединение закрыто окончательно")
		}),
		// Async-ошибки: slow consumer, auth failure, протокольные нарушения —
		// требуют внимания оператора, логируем как ERROR.
		nats.ErrorHandler(func(conn *nats.Conn, sub *nats.Subscription, err error) {
			e := log.Error().Err(err)
			if sub != nil {
				e = e.Str("subject", sub.Subject)
			}
			e.Msg("NATS: async error")
		}),
	}

	// Авторизацию передаём только если оба поля заполнены.
	// Передача пустых credentials на сервер с authorization{} вызовет ошибку подключения,
	// поэтому caller обязан убедиться в корректности конфига до вызова NewClient.
	if cfg.Auth.User != "" && cfg.Auth.Password != "" {
		opts = append(opts, nats.UserInfo(cfg.Auth.User, cfg.Auth.Password))
	}

	nc, err := nats.Connect(cfg.url(), opts...)
	if err != nil {
		return nil, fmt.Errorf("nats: подключение к %s: %w", cfg.url(), err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats: инициализация JetStream: %w", err)
	}

	client := &PlatformClient{
		Conn: nc,
		JS:   js,
		log:  log,
	}

	if cfg.KV.BucketName != "" {
		kvCfg := cfg.KV
		if kvCfg.Replicas <= 0 {
			// Определяем число реплик по числу нод кластера.
			// conn.Servers() возвращает все известные ноды (seed + обнаруженные через INFO).
			kvCfg.Replicas = len(nc.Servers())
			if kvCfg.Replicas < 1 {
				kvCfg.Replicas = 1
			}
			log.Debug().Int("replicas", kvCfg.Replicas).Str("bucket", kvCfg.BucketName).Msg("NATS KV: replicas определены автоматически")
		}
		if err := client.initKV(kvCfg); err != nil {
			nc.Close()
			return nil, err
		}
	}

	log.Info().Str("url", cfg.url()).Msg("NATS: подключено (JetStream: enabled)")
	return client, nil
}

// initKV создаёт KV-бакет или подключается к уже существующему.
// Идемпотентен: повторный вызов при уже существующем бакете безопасен.
//
// Storage всегда FileStorage — данные персистентны между перезапусками,
// что согласовано с jetstream { store_dir: "/var/lib/nats/jetstream" } в nats.conf.
func (p *PlatformClient) initKV(cfg KVConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := p.JS.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:       cfg.BucketName,
		Description:  "Platform Shared State",
		Replicas:     cfg.Replicas,
		History:      cfg.History,
		MaxValueSize: cfg.MaxValueSize,
		Storage:      jetstream.FileStorage, // store_dir: "/var/lib/nats/jetstream" в nats.conf
	})
	if err != nil {
		// Бакет уже существует — просто проверяем доступность.
		if _, kvErr := p.JS.KeyValue(ctx, cfg.BucketName); kvErr != nil {
			return fmt.Errorf("nats: KV-бакет %q недоступен: %w", cfg.BucketName, kvErr)
		}
		p.log.Info().Str("bucket", cfg.BucketName).Msg("NATS KV: бакет уже существует, подключаемся")
		return nil
	}

	p.log.Info().Str("bucket", cfg.BucketName).Int("replicas", cfg.Replicas).Uint8("history", cfg.History).Msg("NATS KV: бакет создан")
	return nil
}

// GetValue возвращает значение ключа из указанного KV-бакета.
// Если ключ не найден, возвращает (nil, nil) — это не ошибка.
func (p *PlatformClient) GetValue(ctx context.Context, bucket, key string) ([]byte, error) {
	kv, err := p.JS.KeyValue(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("nats: GetValue: бакет %q: %w", bucket, err)
	}

	entry, err := kv.Get(ctx, key)
	if err != nil {
		if err == jetstream.ErrKeyNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("nats: GetValue: ключ %q: %w", key, err)
	}

	return entry.Value(), nil
}

// PutValue записывает значение по ключу в указанный KV-бакет.
func (p *PlatformClient) PutValue(ctx context.Context, bucket, key string, value []byte) error {
	kv, err := p.JS.KeyValue(ctx, bucket)
	if err != nil {
		return fmt.Errorf("nats: PutValue: бакет %q: %w", bucket, err)
	}

	if _, err = kv.Put(ctx, key, value); err != nil {
		return fmt.Errorf("nats: PutValue: ключ %q: %w", key, err)
	}

	return nil
}

// WatchKey возвращает KeyWatcher, стримящий все изменения указанного ключа.
// Caller обязан вызвать watcher.Stop() после завершения работы во избежание утечки горутины.
func (p *PlatformClient) WatchKey(ctx context.Context, bucket, key string) (jetstream.KeyWatcher, error) {
	kv, err := p.JS.KeyValue(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("nats: WatchKey: бакет %q: %w", bucket, err)
	}

	watcher, err := kv.Watch(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("nats: WatchKey: ключ %q: %w", key, err)
	}

	return watcher, nil
}

// Close закрывает соединение с NATS. Безопасен при nil-значении Conn.
func (p *PlatformClient) Close() {
	if p.Conn != nil {
		p.Conn.Close()
	}
}

// Drain выполняет graceful shutdown NATS-соединения:
// останавливает приём новых сообщений, дожидается завершения in-flight обработчиков,
// сбрасывает буфер исходящих публикаций и закрывает соединение.
//
// timeout — максимальное время ожидания. По истечении соединение закрывается принудительно.
// Вызывать вместо Close при штатном завершении сервиса.
func (p *PlatformClient) Drain(timeout time.Duration) error {
	if p.Conn == nil || p.Conn.IsClosed() {
		return nil
	}
	if err := p.Conn.Drain(); err != nil {
		return fmt.Errorf("nats: drain: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for !p.Conn.IsClosed() {
		if time.Now().After(deadline) {
			p.Conn.Close()
			return fmt.Errorf("nats: drain превысил таймаут %s, соединение закрыто принудительно", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}
