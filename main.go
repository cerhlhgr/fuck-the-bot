package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"fuck-the-bot/internal/client/ai"
	"fuck-the-bot/internal/client/telegram"
	"fuck-the-bot/internal/controller"
	"fuck-the-bot/internal/migrations"
	"fuck-the-bot/internal/model/postgres"
)

type config struct {
	telegramToken string
	aiKey         string
	model         string
	databaseURL   string
	webhookURL    string
	listenAddr    string
}

func main() {
	cfg := config{
		telegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		aiKey:         os.Getenv("TIMEWEB_AI_API_KEY"),
		model:         os.Getenv("AI_MODEL"),
		databaseURL:   os.Getenv("DATABASE_URL"),
		webhookURL:    os.Getenv("WEBHOOK_URL"),
		listenAddr:    os.Getenv("LISTEN_ADDR"),
	}
	if cfg.telegramToken == "" || cfg.aiKey == "" || cfg.databaseURL == "" {
		log.Fatal("set TELEGRAM_BOT_TOKEN, TIMEWEB_AI_API_KEY and DATABASE_URL")
	}
	if cfg.model == "" {
		cfg.model = "deepseek/deepseek-v4-pro"
	}
	if cfg.listenAddr == "" {
		cfg.listenAddr = ":8080"
	}
	if cfg.webhookURL != "" {
		if err := controller.ValidateWebhookURL(cfg.webhookURL); err != nil {
			log.Fatalf("webhook configuration: %v", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := postgres.OpenPool(ctx, cfg.databaseURL)
	if err != nil {
		log.Fatalf("open PostgreSQL: %v", err)
	}
	defer pool.Close()
	if err := migrations.Up(ctx, pool); err != nil {
		log.Fatalf("apply migrations: %v", err)
	}
	repo := postgres.New(pool)
	if err := repo.Prune(ctx, time.Now()); err != nil {
		log.Fatalf("prune old records: %v", err)
	}
	tg := telegram.New(cfg.telegramToken)
	me, err := tg.GetMe(ctx)
	if err != nil {
		log.Fatalf("getMe: %v", err)
	}
	if me.Username == "" {
		log.Fatal("bot has no username")
	}
	listener, err := net.Listen("tcp", cfg.listenAddr)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.listenAddr, err)
	}
	webhook := controller.NewWebhook(repo)
	server := &http.Server{Handler: webhook, ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	if cfg.webhookURL != "" {
		for ctx.Err() == nil {
			if err := tg.RegisterWebhook(ctx, cfg.webhookURL); err == nil {
				break
			} else {
				log.Printf("setWebhook: %v; retrying in 5 seconds", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	} else {
		log.Print("WEBHOOK_URL is empty; register the public webhook URL with Telegram manually")
	}
	if ctx.Err() != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return
	}
	log.Printf("webhook listening on %s for @%s", cfg.listenAddr, me.Username)

	worker := &controller.Worker{Repo: repo, AI: ai.New(cfg.aiKey, cfg.model), Telegram: tg, Username: me.Username}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		worker.Run(ctx, webhook.Wake())
	}()
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		log.Printf("webhook server: %v", err)
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown webhook server: %v", err)
	}
	workers.Wait()
}
