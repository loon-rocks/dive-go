// Issues runs the issues example app against dive serve.
//
// Usage:
//
//	issues serve [-listen addr]
//	issues start [-dive url] [-tenant t] [-user u] [-role r] [-title text]
//
// The serve subcommand serves the app's Dive tools at /dive/tools/ on
// addr, 127.0.0.1:8080 by default. It verifies run tokens with the PEM
// public key in the file named by APP_PUBLIC_KEY and the issuer in
// APP_ISSUER. Unless DIVE_ENV is production, it also accepts development
// tokens and fails to start without the development key; in production,
// APP_PUBLIC_KEY is required.
//
// The start subcommand starts a run of the file_issue plan on the
// dive serve at url, by default DIVE_URL or http://127.0.0.1:8081, prints
// its entries as they arrive, and prints the plan's result. It signs
// tokens with the PEM private key in the file named by APP_PRIVATE_KEY,
// or with the development key when APP_PRIVATE_KEY is unset.
package main

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	dive "github.com/loon-rocks/dive-go"
	"github.com/loon-rocks/dive-go/examples/issues"
	"github.com/loon-rocks/dive-go/examples/issues/plans"
	"github.com/loon-rocks/dive-go/token"
	"github.com/loon-rocks/dive-go/tools"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx, os.Args[2:])
	case "start":
		err = start(ctx, os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "issues:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: issues serve [-listen addr] | issues start [-dive url] [-tenant t] [-user u] [-role r] [-title text]")
	os.Exit(2)
}

func production() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("DIVE_ENV")), "production")
}

func verifier() (*token.Verifier, error) {
	var appKey crypto.PublicKey
	if path := os.Getenv("APP_PUBLIC_KEY"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if appKey, err = token.ParsePublicKeyPEM(data); err != nil {
			return nil, err
		}
	} else if production() {
		return nil, errors.New("APP_PUBLIC_KEY is required in production")
	}
	v := token.NewVerifier(appKey, token.Issuer(os.Getenv("APP_ISSUER")))
	if !production() {
		dev := token.DevKey()
		if dev.Err() != nil {
			return nil, dev.Err()
		}
		v = v.WithDev(dev)
	}
	return v, v.Err()
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "address to listen on")
	_ = fs.Parse(args)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	v, err := verifier()
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/dive/tools/", issues.DiveTools(issues.NewStore(), log).Handler(tools.HandlerOptions{
		Verifier: v,
		Logger:   log,
	}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("serving dive tools", "listen", *listen, "tools", issues.DiveTools(issues.NewStore(), log).Names())
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func signer() (*token.Signer, error) {
	if path := os.Getenv("APP_PRIVATE_KEY"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		key, err := token.ParsePrivateKeyPEM(data)
		if err != nil {
			return nil, err
		}
		return token.NewSigner(key, token.Issuer(os.Getenv("APP_ISSUER")))
	}
	return token.DevSigner()
}

func start(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	diveURL := fs.String("dive", envOr("DIVE_URL", "http://127.0.0.1:8081"), "dive serve's address")
	tenant := fs.String("tenant", "acme", "tenant to run as")
	user := fs.String("user", "ada@example.com", "user to run as")
	role := fs.String("role", "admin", "role claim for the app's own check")
	title := fs.String("title", "Login fails on Safari", "the issue's title")
	_ = fs.Parse(args)
	s, err := signer()
	if err != nil {
		return err
	}
	startToken, runID, err := s.Start(token.StartClaims{Plan: plans.PlanFileIssue, Tenant: *tenant, User: *user, Extra: map[string]any{"role": *role}})
	if err != nil {
		return err
	}
	fmt.Printf("starting run %s\n", runID)
	client := dive.NewClient(*diveURL)
	p := plans.New(client)
	run, err := p.StartFileIssue(ctx, startToken, plans.FileIssueInput{Title: *title})
	if err != nil {
		return err
	}
	read, err := s.Reader(token.ReadClaims{Tenant: *tenant, User: *user})
	if err != nil {
		return err
	}
	for entry, err := range client.Entries(ctx, read, run.ID) {
		if err != nil {
			return err
		}
		fmt.Printf("%d %s %s\n", entry.Seq, entry.Kind, entry.Path)
	}
	result, view, err := p.WaitFileIssue(ctx, read, run.ID)
	if err != nil {
		if view != nil {
			return fmt.Errorf("run %s ended %s: %s: %w", view.ID, view.Status, view.Error, err)
		}
		return err
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(out))
	return nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
