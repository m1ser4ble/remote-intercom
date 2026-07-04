package main

import (
    "log"
    "net/http"
    "os"
)

func main() {
    addr := env("RELAY_ADDR", ":8080")
    mux := http.NewServeMux()
    mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("ok\n"))
    })
    log.Printf("remote intercom relay listening on %s", addr)
    log.Fatal(http.ListenAndServe(addr, mux))
}

func env(key, fallback string) string {
    if value := os.Getenv(key); value != "" {
        return value
    }
    return fallback
}
