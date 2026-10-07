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
	"fuck-the-bot/internal/client/images"
	"fuck-the-bot/internal/client/search"
	"fuck-the-bot/internal/client/suno"
	"fuck-the-bot/internal/client/telegram"
	"fuck-the-bot/internal/client/tts"
	"fuck-the-bot/internal/controller"
	"fuck-the-bot/internal/migrations"
	"fuck-the-bot/internal/model/postgres"
)

const botModel = "openai/gpt-5.4-nano"

type config struct {
	telegramToken    string
	aiKey            string
	databaseURL      string
	listenAddr       string
	decisionInterval time.Duration
	sunoAPIURL       string
	sunoAPIKey       string
	sunoCallbackURL  string
	braveSearchKey   string
}

func main() {
	cfg := config{
		telegramToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		aiKey:           os.Getenv("TIMEWEB_AI_API_KEY"),
		databaseURL:     os.Getenv("DATABASE_URL"),
		listenAddr:      os.Getenv("LISTEN_ADDR"),
		sunoAPIURL:      os.Getenv("SUNO_API_URL"),
		sunoAPIKey:      os.Getenv("SUNO_API_SECRET_KEY"),
		sunoCallbackURL: os.Getenv("SUNO_API_CALLBACK_URL"),
		braveSearchKey:  os.Getenv("BRAVE_SEARCH_API_KEY"),
	}
	if cfg.telegramToken == "" || cfg.aiKey == "" || cfg.databaseURL == "" {
		log.Fatal("set TELEGRAM_BOT_TOKEN, TIMEWEB_AI_API_KEY and DATABASE_URL")
	}
	if cfg.listenAddr == "" {
		cfg.listenAddr = ":8080"
	}
	cfg.decisionInterval = 30 * time.Second
	if raw := os.Getenv("DECISION_INTERVAL"); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil || interval <= 0 {
			log.Fatal("DECISION_INTERVAL must be a positive Go duration, for example 30s")
		}
		cfg.decisionInterval = interval
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Print("connecting to PostgreSQL")
	pool, err := postgres.OpenPool(ctx, cfg.databaseURL)
	if err != nil {
		log.Fatalf("open PostgreSQL: %v", err)
	}
	defer pool.Close()
	log.Print("PostgreSQL connected")
	if err := migrations.Up(ctx, pool); err != nil {
		log.Fatalf("apply migrations: %v", err)
	}
	log.Print("database migrations applied")
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
	log.Printf("Telegram bot authenticated username=@%s", me.Username)
	var music *controller.MusicCoordinator
	if cfg.sunoAPIURL != "" || cfg.sunoAPIKey != "" || cfg.sunoCallbackURL != "" {
		if cfg.sunoAPIURL == "" || cfg.sunoAPIKey == "" || cfg.sunoCallbackURL == "" {
			log.Fatal("set SUNO_API_URL, SUNO_API_SECRET_KEY and SUNO_API_CALLBACK_URL together")
		}
		sunoClient, err := suno.New(cfg.sunoAPIURL, cfg.sunoAPIKey)
		if err != nil {
			log.Fatalf("configure Suno: %v", err)
		}
		music, err = controller.NewMusicCoordinator(repo, sunoClient, tg, repo, me.Username, cfg.sunoCallbackURL)
		if err != nil {
			log.Fatalf("configure Suno callback: %v", err)
		}
		log.Printf("Suno music generation enabled callback_path=%s", controller.MusicCallbackPath)
	} else {
		log.Print("Suno music generation disabled: set SUNO_API_URL, SUNO_API_SECRET_KEY and SUNO_API_CALLBACK_URL")
	}
	listener, err := net.Listen("tcp", cfg.listenAddr)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.listenAddr, err)
	}
	webhook := controller.NewWebhook(repo)
	router := http.NewServeMux()
	router.Handle(controller.WebhookPath, webhook)
	router.Handle("/healthz", webhook)
	if music != nil {
		router.Handle(controller.MusicCallbackPath, music)
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	log.Printf("webhook listening on %s for @%s", cfg.listenAddr, me.Username)

	log.Printf("AI model decision_and_vision=%s", botModel)
	aiClient := ai.New(cfg.aiKey, botModel, botModel)
	worker := &controller.Worker{Repo: repo, ActionPlans: repo, AI: aiClient, Telegram: tg, Images: images.New(), Photos: tg, Vision: aiClient, Music: music, Voice: tts.New(cfg.aiKey), BotID: me.ID, Username: me.Username}
	if cfg.braveSearchKey != "" {
		worker.Search = search.New(cfg.braveSearchKey)
		log.Print("Brave web, image, video and news search enabled")
	} else {
		log.Print("Brave search disabled: set BRAVE_SEARCH_API_KEY; Wikimedia Commons image search remains available")
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		worker.Run(ctx, cfg.decisionInterval)
	}()
	if music != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			music.RunDelivery(ctx)
		}()
	}
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
