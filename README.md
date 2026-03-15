# Resizer Microservice

Быстрый и надёжный микросервис на Go для изменения размера, обрезки, скругления, сжатия и анализа изображений и **видео** на лету. Поддерживает интеллектуальное кэширование, черновики, детекцию обнажённой натуры, эффект блюра и продвинутые механизмы безопасности.

---

## 🚀 Основные возможности

### Изображения
- **Умный ресайз и кроп** — заполнение области без искажения пропорций (Fill-режим).
- **Скругление углов** — параметр `radius` (0–100%) для аватарок и круглых UI-элементов.
- **Конвертация в WebP / PNG** — настройка качества (`q`), поддержка lossless WebP при `q=100`.
- **Размытие (матовое стекло)** — параметр `blur` (1–99) плавное размытие, `blur=100` — заливка средним цветом.
- **Детекция обнажённой натуры** — на основе анализа цвета кожи с настраиваемым порогом чувствительности.

### Видео
- **stream** (по умолчанию) — обработка на лету через FFmpeg Pipes, без промежуточных файлов на диске.
- **chunked** — параллельное сжатие для максимальной скорости на многоядерных CPU.
- **Обрезка (`start` / `end`)** — извлечение фрагмента из видео.
- **Fragmented MP4** — оптимизация для немедленного воспроизведения в браузере.

### Системные функции
- **Smart Content ID** — SHA-256 от первых 512 КБ для мгновенной дедупликации.
- **Perceptual Hashing** — Average, Perception, Difference хэши для поиска визуально похожих изображений.
- **Info-режим** — параметр `info=1` возвращает JSON-метаданные вместо медиа: хэши, is_nude, upload_date, storage_type, draft_expiration.
- **Draft Storage** — временное хранение перед подтверждением загрузки в облако, с TTL.
- **Локальное / S3 хранилище** — прозрачное переключение через конфиг.
- **Graceful Shutdown** — безопасная остановка с ожиданием завершения текущих задач.

---

## 🛠 Установка

```bash
# FFmpeg — обязателен для видео
brew install ffmpeg           # macOS
apt-get install ffmpeg        # Linux

# WebP — для ускорения кодирования (опционально)
brew install webp pkg-config  # macOS
apt-get install libwebp-dev pkg-config  # Linux

# Сборка
go mod tidy
go build -o resizer .
./resizer
```

---

## ⚙️ Конфигурация (`config.yml`)

```yaml
server:
  port: 8085

security:
  allowed_domains:            # Whitelist доменов для скачивания (пусто = все)
    - example.com
  signature:
    enabled: false            # HMAC-SHA256 защита параметров URL
    allow_sign: true          # Разрешить /sign для генерации подписи
    key: "secret-key"
  nude_check:
    enabled: false            # Включить детекцию глобально
    fail_on_nude: true        # 403 при обнаружении (если false — только X-Nude заголовок)
    blur_on_nude: false       # Применять blur вместо блокировки
    blur_strength: 80         # Сила blur (1–100)
    skin_threshold: 35        # Порог чувствительности: 15=стандарт, 35=баланс, 60=строго

storage:
  type: local                 # local или s3
  path: artefacts
  s3:
    endpoint: ""
    region: us-east-1
    access_key: ""
    secret_key: ""
    bucket: ""
    use_ssl: true
  draft:
    enabled: false            # Временное хранение перед /confirm
    ttl: "1h"                 # TTL черновика по умолчанию
  download:
    forward_headers: false    # Пробрасывать заголовки клиента при скачивании
    user_agent: "Mozilla/5.0..."
    headers:
      Accept-Language: "en-US"

video:
  processing_mode: stream     # stream или chunked

transformations:
  allow_custom: true          # Разрешить произвольные width/height
  presets:
    avatar:   { width: 100, height: 100, radius: 50 }
    thumbnail:{ width: 300, height: 300 }
    cover:    { width: 1280, height: 720, quality: 90 }
```

---

## 📖 API Reference

### GET `/` — Обработка изображения или видео по URL

```
GET /?url={URL}&width={W}&height={H}&format={F}&q={Q}&radius={R}&crop_x={CX}&crop_y={CY}&blur={B}&preset={P}&start={S}&end={E}&nude_check={NC}&nude_blur={NB}&info={I}&draft_ttl={TTL}&s={SIG}
```

| Параметр | Описание | Значения по умолчанию |
|---|---|---|
| `url` | URL изображения или видео | **Обязателен** |
| `width` / `height` | Целевые размеры в пикселях | 0 (сохраняет пропорции) |
| `format` | Формат вывода | `png` / `webp` |
| `q` / `quality` | Качество сжатия | `80` |
| `radius` | Скругление углов (0–100%) | `0` |
| `crop_x` | Стратегия кропа по X | `center` / `left` / `right` |
| `crop_y` | Стратегия кропа по Y | `center` / `top` / `bottom` |
| `blur` | Сила размытия (1–99 = матовое стекло, 100 = заливка цветом) | `0` |
| `preset` | Именованный пресет из конфига | — |
| `start` / `end` | Обрезка видео (секунды, float) | — |
| `nude_check` | Проверить на nudity | `1` / `true` |
| `nude_blur` | Блюрить при nudity (включает nude_check) | `1` / `true` |
| `info` | Вернуть JSON-метаданные вместо медиа | `1` / `true` |
| `draft_ttl` | Сохранить как черновик с TTL | `1h`, `30m`, `2006-01-02` |
| `s` | HMAC-SHA256 подпись параметров | Hex-строка |

**Заголовки ответа:**
- `Content-Type`: `image/webp`, `image/png`, `video/mp4`
- `X-Cache`: `HIT`, `HIT-ID`, `HIT-DRAFT`, `HIT-ID-DRAFT`
- `X-Nude`: `true` (обнаружено) или `blurred` (применён blur)

**Пример — обычный ресайз:**
```bash
curl "http://localhost:8085/?url=https://example.com/photo.jpg&width=400&format=webp&q=85"
```

**Пример — матовый блюр:**
```bash
curl "http://localhost:8085/?url=https://example.com/photo.jpg&width=400&blur=60" -o blurred.png
```

---

### GET `/?info=1` — Получить метаданные без медиафайла

Добавьте `info=1` к любому запросу — вместо изображения/видео придёт JSON.

```json
{
  "hashes": {
    "a_hash": "a:c080bfffffff0404",
    "p_hash": "p:95ed72f8e182c817",
    "d_hash": "d:861674da7ab4bc0c",
    "is_nude": false,
    "upload_date": "2026-03-15T13:31:48+03:00"
  },
  "is_nude": false,
  "is_draft": false,
  "is_cache_hit": true,
  "storage_type": "local",
  "upload_date": "2026-03-15T13:31:48+03:00",
  "path": "/images/photo_w-400_h-0_q-80.png",
  "metadata": {
    "width": 400,
    "height": 135,
    "format": "png"
  }
}
```

При черновике (`draft_ttl`) дополнительно присутствует `draft_expiration`.

---

### POST `/upload` — Загрузка файла из формы

```
POST /upload
Content-Type: multipart/form-data
```

**Параметры формы:**
| Поле | Описание |
|---|---|
| `file` | Файл (**обязателен**) |
| `path` | Подпапка внутри `uploads/` |
| `width`, `height`, `radius`, `q` / `quality`, `format` | Параметры обработки |
| `crop_x`, `crop_y` | Стратегия кропа |
| `blur` | Размытие (1–100) |
| `start`, `end` | Обрезка видео |
| `preset` | Именованный пресет |
| `nude_check`, `nude_blur` | Проверка на nudity |
| `info` | Вернуть JSON вместо медиа |
| `draft_ttl` | TTL черновика |

**Пример:**
```bash
curl -X POST \
  -F "file=@photo.jpg" \
  -F "width=200" \
  -F "format=webp" \
  -F "path=avatars" \
  http://localhost:8085/upload
```

**Пример с info=1:**
```bash
curl -X POST \
  -F "file=@photo.jpg" \
  -F "width=200" \
  -F "info=1" \
  http://localhost:8085/upload
```

---

### POST `/confirm` — Подтверждение черновика

Переносит файл из локального черновика в основное хранилище (S3).

```bash
# По пути
curl -X POST "http://localhost:8085/confirm?path=uploads/abc123_w-200.png"

# По хэшу (подтверждает все варианты)
curl -X POST "http://localhost:8085/confirm?hash=abc123..."
```

**Ответ:**
```json
{ "status": "confirmed", "confirmed": ["uploads/abc123_w-200.png"] }
```

---

### GET `/check` — Проверка наличия файла по хэшу

```bash
curl "http://localhost:8085/check?hash=abc123..."
```

```json
{ "exists": true, "hash": "abc123...", "matches": [{"path": "..."}] }
```

---

### GET `/info` — Статус обработки по URL

```bash
curl "http://localhost:8085/info?url=https://example.com/photo.jpg"
```

```json
{ "exists": true, "url": "...", "hash": "abc123...", "files": ["photo_w-200.png"] }
```

---

### GET `/similar` — Поиск визуально похожих изображений

```bash
curl "http://localhost:8085/similar?url=https://example.com/photo.jpg&threshold=5"
```

| Параметр | Описание |
|---|---|
| `url` | URL изображения для сравнения |
| `threshold` | Расстояние Хэмминга (default `5`, меньше = точнее) |

```json
{
  "url": "...",
  "hash": "p:f8e4c2...",
  "matches": [{ "path": "uploads/similar.png", "distance": 2 }]
}
```

---

### GET `/sign` — Генерация подписанного URL

```bash
curl "http://localhost:8085/sign?url=https://example.com/photo.jpg&width=200"
```

```json
{ "signature": "abc...", "signed_url": "/?url=...&width=200&s=abc..." }
```

---

## 🔐 Безопасность

| Механизм | Описание |
|---|---|
| **HMAC Подпись** | Защита от перебора параметров. Параметр `s` считается по всем query-параметрам кроме самого `s`. |
| **AllowedDomains** | Whitelist доменов — защита от SSRF. |
| **Transformation Presets** | При `allow_custom: false` разрешены только пресеты из конфига. |
| **Nude Detection** | Детекция обнажённой натуры. Настраивается пороговое значение чувствительности (`skin_threshold`). |
| **Graceful Shutdown** | 10 секунд на завершение активных запросов. |

---

## 🎨 Эффект блюра

| `blur` | Поведение |
|---|---|
| `0` | Без изменений |
| `1–99` | Мягкое многопроходное размытие (эффект матового стекла). Чем выше — тем сильнее. |
| `100` | Заливка средним цветом изображения (полная абстракция). |

---

## 🩺 Детекция nudity

Сервис использует библиотеку `go-nude` для анализа распределения skin-пикселей.

| Параметр | Описание |
|---|---|
| `nude_check=1` | Проверить изображение в запросе |
| `nude_blur=1` | Блюрить при обнаружении (включает nude_check) |
| `X-Nude: true` | Заголовок ответа — обнаружено |
| `X-Nude: blurred` | Заголовок ответа — применён blur |

**Настройка чувствительности** в `config.yml`:
```yaml
nude_check:
  skin_threshold: 35  # 15=высокая чувствительность, 35=баланс, 60=строгий режим
```

Чем выше порог — тем меньше ложных срабатываний на портретах и фото с людьми.
