# Resizer Microservice

Быстрый и надежный микросервис на Go для загрузки, изменения размера (ресайза), обрезки, скругления краев и сжатия изображений и **видео** "на лету". Поддерживает интеллектуальное кэширование, потоковую обработку и продвинутые механизмы безопасности.

## 🚀 Основные возможности

### Изображения
*   **Умный ресайз и кроп**: Автоматическое заполнение области без искажения пропорций.
*   **Скругление углов**: Параметр `radius` (0-100%) для создания аватарок и круглых элементов.
*   **Конвертация в WebP**: Поддержка современного формата с управлением качеством (`q`).

### Видео
*   **Два режима обработки**: 
    1.  **stream** (по умолчанию) — обработка "на лету" через FFmpeg Pipes. Экономит диск, подходит для файлов любого размера.
    2.  **chunked** — параллельное сжатие. Видео разбивается на части и обрабатывается во множество потоков (максимальная скорость для многоядерных CPU).
*   **Обрезка (Trimming)**: Параметры `start` и `end` для выделения фрагмента из целого видео.
*   **Fragmented MP4**: Оптимизировано для немедленного воспроизведения в браузере.

### Системные функции
*   **Smart Content ID & Perceptual Hashing**: Хэширование первых 512 КБ файла для мгновенного поиска дубликатов + **перцептивные хэши** (Average, Perception, Difference) для поиска визуально похожих изображений.
*   **Многоуровневое кэширование**: Сохранение результатов на диск или в S3 с учетом всех параметров обработки и метаданных.
*   **Draft Storage**: Поддержка временного хранения (черновиков) перед окончательной загрузкой в облако.
*   **Graceful Shutdown**: Безопасная остановка сервера с ожиданием завершения текущих задач.

## 🛠 Установка и зависимости

1.  **FFmpeg** (Обязательно для видео):
    - macOS: `brew install ffmpeg`
    - Linux: `sudo apt-get install ffmpeg`
2.  **WebP** (Опционально для ускорения):
    - macOS: `brew install webp pkg-config`
    - Linux: `sudo apt-get install libwebp-dev pkg-config`

### Запуск
```bash
go mod tidy
go build -o resizer main.go
./resizer
```

## ⚙️ Конфигурация (`config.yml`)

```yaml
server:
  port: 8085
security:
  allowed_domains: ["example.com"]
  signature:
    enabled: true           # Проверка подписи HMAC (рекомендуется для продакшена)
    allow_sign: false       # Разрешить ли эндпоинт /sign (лучше выклоючать в прод)
    key: "secret-key"       # Ключ для подписи параметров
storage:
  type: local               # Режимы: local, s3
  path: "artefacts"         # Путь для локального кэша
  s3:
    endpoint: "s3.aws.com"  # URL S3-совместимого хранилища
    bucket: "my-cache"
    access_key: "..."
    secret_key: "..."
    use_ssl: true
  draft:                    # Режим черновика (только для S3)
    enabled: true           # Сохранять локально до вызова /confirm
    ttl: "1h"               # Время жизни черновика по умолчанию
  download:
    user_agent: "Mozilla/5.0..." # Кастомный User-Agent для скачивания
    forward_headers: false   # Пробрасывать ли заголовки клиента
  nude_check:                # Детекция обнаженной натуры
    enabled: false           # Включить глобально
    fail_on_nude: true       # Ошибка 403 при обнаружении
video:
  processing_mode: stream   # Режимы: stream, chunked
transformations:
  allow_custom: true        # Разрешать ли произвольные width/height
  presets:                  # Именованные наборы параметров
    avatar: { width: 100, height: 100, radius: 50, quality: 90 }
    thumbnail: { width: 300, height: 300 }
```

## 📖 API Reference

### 1. Основной эндпоинт обработки (`/`)
`GET /?url={URL}&width={W}&height={H}&format={F}&q={Q}&start={S}&end={E}&preset={P}&s={SIG}`

| `s` | HMAC-SHA256 подпись параметров | Hex строка |

**Ответ:**
*   **Успех**: Бинарные данные (изображение или видео).
*   **Заголовки**:
    *   `Content-Type`: `image/webp`, `image/png` или `video/mp4`.
    *   `X-Cache`: `HIT` (из кеша), `HIT-ID` (по контенту), `MISS` (новая обработка).
    *   `X-Nude`: `true` (если обнаружена обнаженная натура).

### 2. Подтверждение черновика (`/confirm`)
Переносит файл из локальной папки черновиков в основное хранилище (S3).
`POST /confirm?path={relative_path}` или `POST /confirm?hash={content_id}`

Пример (по пути):
`curl -X POST "http://localhost:8085/confirm?path=images/photo_resized-200x200.png"`

Пример (по хэшу — подтверждает все варианты этого изображения):
`curl -X POST "http://localhost:8085/confirm?hash=a1b2c3d4..."`

**Ответ (JSON):**
```json
{
  "status": "confirmed",
  "hash": "a1b2c3d4...",
  "confirmed": [
    "images/photo_resized-200x200.png",
    "images/photo_resized-400x400.webp"
  ]
}
```

### 2. Проверка хэша (`/check`)
Узнать, есть ли файл с таким Smart Content ID в системе. Возвращает список всех найденных вариантов (размеров/форматов).
`GET /check?hash={hex_hash}`

**Ответ (JSON):**
```json
{
  "exists": true,
  "hash": "a1b2c3d4...",
  "matches": [
    { "path": "path/to/image_w-200_h-200.png", "size": 0 }
  ]
}
```

### 3. Инфо о ссылке (`/info`)
Узнать статус обработки и хэш для конкретного URL.
`GET /info?url={URL}`

**Ответ (JSON):**
```json
{
  "exists": true,
  "url": "http://example.com/image.jpg",
  "hash": "a1b2c3d4...",
  "files": [
    "image_resized-200x200.png",
    "image_resized-200x200.png.hashes"
  ]
}
```

### 4. Поиск похожих изображений (`/similar`)
Находит уже обработанные изображения, которые визуально похожи на переданное. Использует расстояние Хэмминга по перцептивному хэшу.
`GET /similar?url={URL}&threshold={T}`

| Параметр | Описание | Значение |
| :--- | :--- | :--- |
| `url` | Ссылка на изображение для сравнения | URL |
| `threshold` | Порог сходства (расстояние Хэмминга) | 5 (по умолчанию), меньше = точнее |

**Ответ (JSON):**
```json
{
  "url": "http://example.com/target.jpg",
  "hash": "f8e4c2...", 
  "matches": [
    { "path": "already/processed/similar_img.png", "distance": 2 }
  ]
}
```

### 5. Генерация подписи (`/sign`)
(Если включено в конфиге) Вспомогательный метод для получения подписи.
`GET /sign?url=...&width=...`

**Ответ (JSON):**
```json
{
  "signature": "abcd1234...",
  "signed_url": "/?url=http%3A%2F%2Fex.com%2Fimg.jpg&width=200&s=abcd1234..."
}
```

## 🔐 Безопасность

*   **HMAC Подпись**: Защищает от перебора параметров злоумышленниками. Подпись считается от всех query-параметров, отсортированных по имени ключа.
*   **Transformation Presets**: Позволяют ограничить доступные размеры только разрешенными в конфиге при `allow_custom: false`.
*   **SSRF Protection**: Проверка `AllowedDomains`.
*   **OOM Protection**: Лимиты на размер скачивания и использование потоков.
