# 🚀 AI-Powered Media Intelligence & Processing Microservice

**Resizer** — это не просто ресайзер. Это высокопроизводительный швейцарский нож для работы с медиаконтентом, объединяющий в себе классический имидж-процессинг и мощь глубокого обучения (Deep Learning).

Построенный на Go и ONNX Runtime, сервис позволяет не только изменять размеры, но и **«понимать» контент**, обеспечивая безопасность и автоматизацию на уровне enterprise-решений.

---

## 🔥 Почему именно Resizer?

### 1. 📂 Умная модерация 24/7 (UGC Safety)
Больше не нужно проверять контент вручную. Встроенная нейросеть анализирует каждое изображение на наличие нежелательного контента (Porn, Hentai, Sexy). Вы можете автоматически блокировать такие файлы или накладывать эффект блюра на лету.

### 2. 🧠 Автоматическое описание (SEO & Accessibility)
Сервис использует **MobileNetV2** для распознавания объектов. Получайте текстовое описание того, что изображено на картинке (например, *"Egyptian cat, sports car"*), автоматически генерируйте `alt`-теги для SEO и улучшайте доступность вашего продукта.

### 3. ⚡️ Невероятная скорость видео (FFmpeg Hybrid)
Обработка видео поддерживает два режима:
- **Streaming**: Обработка «на лету» через пайпы без записи на диск.
- **Chunked Parallel**: Параллельное сжатие фрагментов для максимальной скорости на многоядерных CPU.
- **Fragmented MP4**: Оптимизация видеопотока для немедленного воспроизведения в браузере без ожидания загрузки всего файла.

### 4. 🔍 Визуальный поиск и дедупликация
Благодаря **Perceptual Hashing** (Average, Perception, Difference), сервис находит визуально похожие изображения, даже если они были пережаты или изменены. **Smart Content ID** (SHA-256 от первых 512 КБ) обеспечивает мгновенную дедупликацию идентичных файлов.

---

## 🚀 Основные возможности

### Изображения
- **Умный ресайз и кроп**: Заполнение области без искажения пропорций (Fill-режим) и адаптивные стратегии кропа.
- **Матовое стекло (Frosting)**: Продвинутый многопроходный блюр (`blur=1..99`) или абстрактная заливка средним цветом (`blur=100`).
- **WebP & Lossless**: Современное сжатие. Поддержка **lossless WebP** при весовом коэффициенте `q=100`.
- **Aura Radius**: Скругление углов (0–100%) для создания идеальных UI-компонентов и аватарок.

### Безопасность и Инфраструктура
- **HMAC Signature**: Защита от перебора параметров (Signature Tampering) и DDoS-атак на процессинг.
- **Allowed Domains**: Безопасный Whitelist доменов для предотвращения SSRF-атак.
- **Draft & Confirm**: Двухэтапная загрузка с TTL — временное хранение перед подтверждением.
- **S3-Native**: Прозрачная работа с локальным хранилищем или любым S3-совместимым облаком.
- **Graceful Shutdown**: Безопасная остановка (10 сек) с ожиданием завершения текущих задач.

---

## 📖 API & Быстрый старт

### GET `/` — Процессинг на лету
```bash
# Получить описание и статус безопасности
curl "http://localhost:8085/?url=https://site.com/img.jpg&info=1"

# Сделать превью 200x200 со скруглением и безопасным блюром
curl "http://localhost:8085/?url=...&width=200&height=200&radius=100&nude_blur=1"
```

### Пример JSON Info (`info=1`):
```json
{
   "description" : "Neutral, Egyptian cat, tiger cat",
   "is_nude" : false,
   "is_cache_hit" : true,
   "hashes" : {
      "a_hash" : "a:ffb7c3c3c3838303",
      "p_hash" : "p:bcca96c0f1f1b1b0",
      "d_hash" : "d:14273f1f17373e3e"
   },
   "metadata" : { "format" : "webp", "width" : 200, "height" : 200 },
   "path" : "/storage/abc123_w200_radius100.webp"
}
```

**Описание полей ответа:**
- `description`: Текстовое описание содержимого. Включает топ-категорию NSFW и список распознанных объектов через запятую.
- `is_nude`: Булево значение (`true`/`false`). Указывает на обнаружение заблокированного контента.
- `is_cache_hit`: `true`, если результат взят из кэша.
- `metadata`: Реальные размеры (`width`/`height`) и расширение файла.
- `hashes`: Хэши для дедупликации (`a_hash`, `p_hash`, `d_hash`).

---

## 📖 Полный API Reference

### GET `/` — Параметры запроса

| Параметр | Описание | Значения по умолчанию |
|---|---|---|
| `url` | URL медиафайла | **Обязателен** |
| `width` / `height` | Целевые размеры в пикселях | 0 (сохраняет пропорции) |
| `format` | Формат вывода: `png` / `webp` | `png` |
| `q` | Качество сжатия (1-100). При `q=100` — lossless WebP. | `80` |
| `radius` | Скругление углов (0–100%) | `0` |
| `crop_x` | Кроп по X: `center`, `left`, `right` | `center` |
| `crop_y` | Кроп по Y: `center`, `top`, `bottom` | `center` |
| `blur` | Размытие (1–99 = стекло, 100 = заливка цветом) | `0` |
| `preset` | Именованный пресет из конфига | — |
| `start` / `end` | Обрезка видео (секунды, float) | — |
| `nude_check` | Включить AI-детекцию наготы | `false` |
| `nude_blur` | Блюрить при наготе (включает nude_check) | `false` |
| `info` | Вернуть JSON вместо медиа | `false` |
| `draft_ttl` | Черновик с TTL (`24h`, `2026-01-02T...`) | — |
| `s` | HMAC-SHA256 подпись параметров | — |

**Заголовки ответа:**
- `X-Cache`: `HIT`, `HIT-ID`, `HIT-DRAFT`, `HIT-ID-DRAFT`
- `X-Nude`: `true` (обнаружено) или `blurred` (авто-блюр)

---

### POST `/upload` — Загрузка файла
Принимает `multipart/form-data`. Поля:
- `file`: Бинарный файл (**Обязательно**).
- `path`: Кастомная подпапка (например, `users/123/avatars/`).
- Все параметры из GET-запроса (width, blur и т.д.).

---

### Другие методы
- **POST `/confirm`**: `path={PATH}` или `hash={HASH}`. Подтверждает черновик, перенося его в постоянное хранилище.
- **GET `/check`**: `hash={HASH}`. Быстрая проверка наличия файла по контент-хэшу.
- **GET `/info`**: `url={URL}`. Статус и список созданных файлов для конкретного URL.
- **GET `/similar`**: `url={URL}&threshold=5`. Поск визуально похожих изображений.
- **GET `/sign`**: `url={...}&width=...`. Генерация подписанного URL (для `security.signature.allow_sign: true`).

---

## ⚙️ Конфигурация (`config.yml`)

```yaml
server:
  port: 8085

security:
  allowed_domains: ["example.com"]   # SSRF Protection
  signature:
    enabled: true                   # HMAC Protection
    key: "y0ur-secret-key"
  nude_check:
    enabled: true
    fail_on_nude: false             # 403 при обнаружении
    blur_on_nude: true              # Blur вместо блокировки
    blocked_categories: ["Porn", "Hentai"]
    threshold: 0.5                  # AI Confidence

storage:
  type: local                       # local или s3
  path: artefacts
  s3:
    endpoint: "s3.amazonaws.com"
    bucket: "media-bucket"
  draft:
    enabled: true
    ttl: "1h"

transformations:
  allow_custom: true                # Разрешить произвольные размеры
  presets:
    avatar: { width: 100, height: 100, radius: 50 }
```

---

## 🛠 Установка и Запуск

```bash
# Сборка
go mod tidy
go build -o resizer .

# Запуск
./resizer --config config.yml
```

> [!IMPORTANT]
> Для работы ИИ и видео установите `ffmpeg` и положите ONNX модели в `/models`:
> - `models/nsfw_mobilenet.onnx`
> - `models/mobilenetv2-10.onnx`
