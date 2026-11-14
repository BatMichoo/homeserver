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

	"github.com/BatMichoo/homeserver/auth/internal/config"
	"github.com/BatMichoo/homeserver/auth/internal/db"
	"github.com/BatMichoo/homeserver/auth/internal/handlers"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Getenv))
}

// run is main without the process around it, for tests.
func run(args []string, out io.Writer, getenv func(string) string) int {
	cfg, err := config.Load(getenv)
	if err != nil {
		fmt.Fprintln(out, "config:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DB)
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	defer database.Close()

	if len(args) == 0 || args[0] == "serve" {
		return serve(ctx, cfg, database)
	}
	fmt.Fprintf(out, "unknown command %q\n", args[0])
	return 2
}

func serve(ctx context.Context, cfg *config.Config, database *db.DB) int {
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handlers.NewRouter(handlers.Deps{DB: database}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("auth listening on %s", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
		return 1
	}
	return 0
}
