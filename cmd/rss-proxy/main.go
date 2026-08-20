// Command rss-proxy runs the podcast HTTPS-to-HTTP compatibility proxy.
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/robindiddams/rss-proxy/internal/config"
	"github.com/robindiddams/rss-proxy/internal/server"
)

func main() {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rss-proxy: %v\n", err)
		os.Exit(2)
	}
	logf := func(format string, args ...any) {
		log.Printf(format, args...)
	}
	srv, err := server.New(&cfg, logf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rss-proxy: %v\n", err)
		os.Exit(2)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil {
			fmt.Fprintf(os.Stderr, "rss-proxy: %v\n", err)
			os.Exit(1)
		}
	case sig := <-sigCh:
		logf("rss-proxy: received %s, shutting down", sig)
	}
}
