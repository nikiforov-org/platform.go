# TODO

## ✅ ИСПРАВЛЕНО: NATS Single-Node JetStream Issue

**Статус:** Решено 2026-05-03

**Проблема:**
При установке первой ноды через `deployments/envs/prod/setup.sh` NATS запускался с конфигом, содержащим `cluster {}` блок. JetStream требовал Raft-кворум для работы, но одиночная нода не могла образовать кворум сама с собой.

**Решение: GitHub Actions Clustering**

### Архитектура

- **setup.sh** — устанавливает ноды **всегда в standalone-режиме** (без `cluster {}`)
- **GitHub Actions workflow (`clustering.yml`)** — управляет кластеризацией централизованно

### Поведение

1. **Одна нода:** NATS работает в standalone, JetStream функционирует без кворума
2. **Две+ ноды:** После деплоя автоматически запускается `clustering.yml`:
   - Получает список нод из DNS
   - Создаёт `/etc/nats/cluster.conf` на всех нодах
   - Добавляет `include "cluster.conf"` в `/etc/nats/nats.conf`
   - Выполняет `systemctl reload nats`
   - NATS образует кластер, JetStream переходит в multi-node Raft

### Выполненные изменения

- ✅ Обновлён `deployments/envs/prod/setup.sh` — убран `cluster {}` из HEREDOC `/etc/nats/nats.conf`
- ✅ Обновлён `deployments/infra/nats/nats.conf` — убран `cluster {}` блок
- ✅ Создан `.github/workflows/clustering.yml` — управление кластеризацией
- ✅ Обновлён `CLAUDE.md` — добавлена информация о новой архитектуре
- ✅ Мигрирован тестовый сервер `3.64.192.171` — теперь работает в standalone-режиме

### Преимущества

✅ **setup.sh предельно прост** — нет логики определения первой/второй ноды  
✅ **SSH уже настроен в GitHub Actions** — используем существующие секреты  
✅ **Централизованное управление** — вся кластер-логика в одном месте  
✅ **Идемпотентность** — workflow можно запускать повторно  
✅ **Логи доступны** — все SSH-операции видны в GitHub Actions UI  
✅ **Manual trigger** — можно вручную перекластеризовать через GitHub UI  

### Ручной запуск кластеризации

```bash
# Через GitHub UI:
# Actions → NATS Clustering → Run workflow → main → Run workflow
```

### Проверка состояния

```bash
# На любой ноде:
curl -s http://127.0.0.1:8222/healthz
# Ожидается: {"status":"ok"}

# Проверка кластера (для 2+ нод):
curl -s http://127.0.0.1:8222/varz | grep -E 'num_routes|cluster'
# Ожидается: "num_routes": N-1 (для N нод)
```

---

## Следующие шаги

1. **Протестировать workflow** — добавить вторую ноду, проверить автоматическую кластеризацию
2. **Обновить документацию** — добавить секцию в `deployments/envs/prod/prod.md` о NATS-кластеризации
3. **Продолжить аудит** — вернуться к `audit/STATUS.md` после стабилизации production-инфраструктуры
