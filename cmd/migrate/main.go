package main

import (
	"context"
	"log"
	"os"
	"time"

	"fuck-the-bot/internal/migrations"
	"fuck-the-bot/internal/model/postgres"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "down") {
		log.Fatal("usage: go run ./cmd/migrate [up|down]")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("set DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := postgres.OpenPool(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if os.Args[1] == "up" {
		err = migrations.Up(ctx, pool)
	} else {
		err = migrations.Down(ctx, pool)
	}
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("migrations %s complete", os.Args[1])
}
