package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/remote-intercom/remote-intercom/relay/internal/auth"
	"github.com/remote-intercom/remote-intercom/relay/internal/channel"
	"github.com/remote-intercom/remote-intercom/relay/internal/httpapi"
)

const version = "0.1.0"

func main() {
	addr := env("RELAY_ADDR", ":8080")
	secret := []byte(os.Getenv("RELAY_TOKEN_SECRET"))
	if len(secret) == 0 {
		var err error
		secret, err = devTokenSecret()
		if err != nil {
			log.Fatalf("generate dev token secret: %v", err)
		}
		log.Printf("RELAY_TOKEN_SECRET is not set; using generated development token secret")
	}

	tokens, err := auth.NewTokenManager(secret, 24*time.Hour)
	if err != nil {
		log.Fatalf("configure token manager: %v", err)
	}
	server := httpapi.NewServer(channel.NewRegistry(), tokens, version)

	log.Printf("remote intercom relay listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, server.Routes()))
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func devTokenSecret() ([]byte, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	return []byte(hex.EncodeToString(b[:])), nil
}
