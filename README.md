# Telegram-бот для ответов на @упоминания

Telegram отправляет сообщения на HTTPS webhook бота. Бот быстро сохраняет обновление в PostgreSQL, а затем обрабатывает его: хранит короткие тексты переписки за последние 24 часа и отвечает через Timeweb AI на упоминание своего `@username`. Контекст берётся только из того же чата и темы. Ответ ИИ ожидается в JSON вида `{"reply":"..."}` и публикуется реплаем.

## Запуск в Timeweb App Platform

Выберите фреймворк **Docker Compose** и файл `docker-compose.yml` из корня репозитория. В настройках приложения задайте `TELEGRAM_BOT_TOKEN`, `TIMEWEB_AI_API_KEY`, `DATABASE_URL`, `WEBHOOK_SECRET` и `WEBHOOK_DOMAIN`. В `WEBHOOK_DOMAIN` укажите публичный домен приложения из панели Timeweb без `https://`. Используется внешняя PostgreSQL из `DATABASE_URL`.

В манифесте Timeweb есть только сервис `bot`: платформа проксирует его порт `8080` и обеспечивает HTTPS. Отдельный контейнер `migrate` не нужен — корневой `main.go` применяет миграции до запуска webhook. После обновления репозитория запустите новый деплой и убедитесь, что в списке контейнеров нет `migrate`.

Если в логах нового деплоя всё ещё появляется `migrate-1`, проверьте подключённую ветку и выбранный фреймворк Docker Compose в настройках Timeweb. Такого сервиса нет в актуальном `docker-compose.yml`; значит деплой использует другую конфигурацию. Передайте поддержке Timeweb идентификатор ошибки из журнала и попросите удалить старый сервис `migrate` из конфигурации приложения.

## Запуск на своём сервере через Docker Compose

1. Создайте бота через [@BotFather](https://t.me/BotFather). Чтобы бот видел всю переписку группы, отключите **Group Privacy Mode** через `/setprivacy` или назначьте бота администратором. После изменения privacy mode удалите бота из группы и добавьте заново.
2. Направьте DNS-запись `A` выбранного домена на публичный IPv4 сервера. Откройте входящие порты 80 и 443. Caddy в Compose сам получит HTTPS-сертификат.
3. Скопируйте `.env.example` в `.env`. Заполните `TELEGRAM_BOT_TOKEN`, `TIMEWEB_AI_API_KEY`, `WEBHOOK_DOMAIN` и `WEBHOOK_SECRET`. Секрет можно создать командой `openssl rand -hex 32`; домен в `WEBHOOK_DOMAIN` указывайте без `https://`.
4. Запустите `docker compose -f compose.local.yaml up -d --build`. Посмотрите журнал: `docker compose -f compose.local.yaml logs -f bot caddy`.

5. После записи `webhook listening ... for @имя_бота` напишите в группе `@имя_бота привет`.

Для внешней PostgreSQL укажите `DATABASE_URL` в `.env`. Контейнер бота использует этот адрес; локальная БД из Compose при этом запускается, но не используется.

Бот сам вызывает `setWebhook` с адресом `https://<WEBHOOK_DOMAIN>/telegram/webhook` и секретом. Публичный HTTP-приёмник находится за Caddy; входящий запрос принимается только с верным заголовком `X-Telegram-Bot-Api-Secret-Token`. Long polling в этой версии не используется. Запускайте один экземпляр бота с одним Telegram-токеном.

PostgreSQL хранится в Docker volume `postgres_data`. Для подключения к базе с хоста доступен `127.0.0.1:25432` (порт меняется через `POSTGRES_PORT`). При запуске бот применяет версионированные SQL-миграции из `internal/migrations/sql/`. Входящие обновления сначала сохраняются в `bot_updates`, поэтому после перезапуска обработка продолжается. Обработанные обновления удаляются через 48 часов. История сообщений старше 24 часов удаляется каждый час.

Для экономии контекста каждая реплика сокращается до 320 символов, а в запрос ИИ попадают не более 80 последних реплик и 12 000 символов. Сохраняются сообщения, которые Telegram передал после запуска бота; прежнюю историю Telegram через Bot API не выдаёт. При включённом privacy mode контекст группы будет неполным.

## Запуск Go-процесса отдельно

Если у вас уже есть PostgreSQL и HTTPS reverse proxy, направьте путь `/telegram/webhook` на локальный HTTP-порт бота и задайте:

```sh
export TELEGRAM_BOT_TOKEN='токен_от_BotFather'
export TIMEWEB_AI_API_KEY='ключ_Timeweb_AI'
export DATABASE_URL='postgres://bot:пароль@127.0.0.1:5432/bot?sslmode=disable'
export WEBHOOK_URL='https://bot.example.com/telegram/webhook'
export WEBHOOK_SECRET='случайный_секрет'
export LISTEN_ADDR=':8080' # необязательно
go run .
```

Публичный адрес должен использовать HTTPS. Telegram поддерживает для webhook порты 443, 80, 88 и 8443. Локальный HTTP-порт `8080` остаётся за reverse proxy.

## Миграции и структура проекта

Корневой `main.go` автоматически применяет миграции перед запуском webhook и обработки сообщений. Отдельный контейнер или команда для миграций не требуются. SQL-файлы находятся в `internal/migrations/sql/`.

- `internal/model/` — Telegram-сообщения, история и интерфейс хранилища; `internal/model/postgres/` — реализация модели в PostgreSQL.
- `internal/controller/` — HTTP webhook и обработка очереди обновлений.
- `internal/view/` — текст системного промпта, формат переписки и разбиение ответов для Telegram.
- `internal/client/` — клиенты Timeweb AI и Telegram Bot API; корневой `main.go` — запуск и соединение компонентов.

## Проверка

`go test ./...` проверяет обработчик и формат сообщений. Для SQL-проверки на отдельной тестовой базе задайте `TEST_DATABASE_URL` и запустите `go test ./...`. Статус зарегистрированного webhook можно проверить методом Telegram `getWebhookInfo`.
