# Telegram-бот для ответов на @упоминания

Бот принимает обновления Telegram через `POST /telegram/webhook`, сохраняет сообщения в PostgreSQL и отвечает на упоминания через Timeweb AI. В запрос ИИ передаётся сокращённая переписка за последние 24 часа из того же чата и темы.

## Настройка

Обязательные переменные окружения:

```dotenv
TELEGRAM_BOT_TOKEN=токен_от_BotFather
TIMEWEB_AI_API_KEY=ключ_Timeweb_AI
DATABASE_URL=postgres://user:password@host:5432/database?sslmode=require
```

Дополнительные переменные:

```dotenv
AI_MODEL=deepseek/deepseek-v4-pro
LISTEN_ADDR=:8080
```

После публикации приложения один раз зарегистрируйте его публичный HTTPS-адрес в Telegram (замените `bot.example.com` на домен приложения):

```sh
curl --request POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/setWebhook" \
  --data-urlencode "url=https://bot.example.com/telegram/webhook" \
  --data-urlencode 'allowed_updates=["message"]'
```

Бот сам не вызывает `setWebhook`. Хендлер слушает путь `/telegram/webhook`, но наличие хендлера не сообщает Telegram публичный адрес.

`WEBHOOK_SECRET` больше не используется. Хендлер принимает POST без проверки заголовка `X-Telegram-Bot-Api-Secret-Token`; публичную ручку может вызвать любой, кто знает её адрес. Ограничение размера тела и проверка JSON остаются.

Для запуска Go-процесса задайте переменные в окружении и выполните `go run .`. Приложение слушает HTTP на `:8080`; публичный HTTPS обеспечивает ваш reverse proxy или платформа размещения. При запуске `main.go` применяет SQL-миграции из `internal/migrations/sql/` до обработки обновлений.

Чтобы бот видел переписку группы, отключите **Group Privacy Mode** через [@BotFather](https://t.me/BotFather) или назначьте бота администратором. После изменения режима удалите бота из группы и добавьте заново. Старая история Telegram через Bot API недоступна.

## Данные и проверка

Логи пишутся в стандартный вывод и доступны в журнале приложения Timeweb. Для каждого запроса видны `webhook request received` и результат `webhook accepted` либо `webhook request rejected` / `webhook queue failed`. Обработчик пишет `update_id`, `chat_id`, этап запроса к ИИ и отправки ответа. Ошибки содержат этап и причину; токены и текст переписки в лог не выводятся.

Входящие обновления сохраняются в `bot_updates`, поэтому после перезапуска обработка продолжается. Обработанные обновления удаляются через 48 часов; сообщения старше 24 часов удаляются каждый час. Каждая реплика сокращается до 320 символов, а в запрос ИИ попадают не более 80 последних реплик и 12 000 символов.

`go test ./...` проверяет обработку обновлений и формат сообщений. Для SQL-проверки на отдельной тестовой базе задайте `TEST_DATABASE_URL`. Состояние регистрации webhook можно проверить методом Telegram `getWebhookInfo`.
