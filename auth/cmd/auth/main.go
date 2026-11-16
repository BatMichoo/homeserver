// Command auth is the home server's login service.
//
//	auth                            serve (default)
//	auth reset-password <username>  print a new password for a user
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BatMichoo/homeserver/auth/internal/accounts"
	"github.com/BatMichoo/homeserver/auth/internal/config"
	"github.com/BatMichoo/homeserver/auth/internal/db"
	"github.com/BatMichoo/homeserver/auth/internal/handlers"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DB)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer database.Close()
	acc := accounts.New(database, cfg.SessionTTL)

	args := os.Args[1:]
	if len(args) == 0 || args[0] == "serve" {
		os.Exit(serve(ctx, cfg, database, acc))
	}
	os.Exit(command(ctx, args, os.Stdout, acc))
}

// command runs a one-off command and returns the exit code.
func command(ctx context.Context, args []string, out io.Writer, acc *accounts.Service) int {
	switch {
	case len(args) == 2 && args[0] == "reset-password":
		password, err := acc.ResetPassword(ctx, args[1])
		if errors.Is(err, accounts.ErrUnknownUser) {
			fmt.Fprintf(out, "No user called %q.\n", args[1])
			return 1
		}
		if err != nil {
			fmt.Fprintln(out, "reset-password:", err)
			return 1
		}
		fmt.Fprintf(out, "New password for %s: %s\n", args[1], password)
		return 0
	default:
		fmt.Fprintln(out, "usage: auth [serve] | auth reset-password <username>")
		return 2
	}
}

func serve(ctx context.Context, cfg *config.Config, database *db.DB, acc *accounts.Service) int {
	router, err := handlers.NewWebRouter(handlers.Deps{
		DB: database, Accounts: acc, LanDomain: cfg.LanDomain, PublicURL: cfg.PublicURL(),
		HomeServerURL: cfg.HomeServerURL(),
	})
	if err != nil {
		log.Print(err)
		return 1
	}
	go sweep(ctx, acc)

	srv := &http.Server{Addr: cfg.Addr, Handler: router, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("auth listening on %s (%s)", cfg.Addr, cfg.PublicURL())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
		return 1
	}
	return 0
}

// sweep deletes expired sessions every hour, and forgets old failed
// logins every minute.
func sweep(ctx context.Context, acc *accounts.Service) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	prune := time.NewTicker(time.Minute)
	defer prune.Stop()
	sweepSessions := func() {
		if n, err := acc.Sweep(ctx); err != nil {
			log.Printf("sweep sessions: %v", err)
		} else if n > 0 {
			log.Printf("removed %d expired sessions", n)
		}
	}
	sweepSessions()
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			acc.PruneThrottle()
		case <-t.C:
			sweepSessions()
		}
	}
}
