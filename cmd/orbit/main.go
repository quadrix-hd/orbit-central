package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/quadrix-hd/orbit-central/internal/orbit"
)

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "version" {
		fmt.Println(orbit.Version)
		return nil
	}
	if command == "healthcheck" {
		client := http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://127.0.0.1:" + env("PORT", "8080") + "/api/health")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("healthcheck: HTTP %d", response.StatusCode)
		}
		return nil
	}
	if command != "serve" {
		return orbit.CLI(os.Args[1:], env("ORBIT_DATA_DIR", "./data"))
	}
	if err := orbit.ValidateTrustedProxies(os.Getenv("ORBIT_TRUSTED_PROXIES")); err != nil {
		return err
	}
	unlock, err := orbit.LockData(env("ORBIT_DATA_DIR", "./data"))
	if err != nil {
		return err
	}
	defer unlock()
	store, err := orbit.OpenStore(env("ORBIT_DATA_DIR", "./data"))
	if err != nil {
		return err
	}
	defer store.DB.Close()
	pve, err := orbit.NewProxmox(store, os.Getenv("ORBIT_CA_FILE"))
	if err != nil {
		return err
	}
	handler := orbit.NewServer(store, pve, env("ORBIT_PUBLIC_ORIGIN", "http://localhost:8080"), env("ORBIT_STATIC_DIR", "./dist"))
	server := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	handler.RunWorkers(ctx)
	defer func() { stop(); handler.WaitWorkers() }()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("Orbit %s Go ready", orbit.Version)
	select {
	case err := <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
