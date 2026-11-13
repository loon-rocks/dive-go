package token_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log"

	"github.com/loon-rocks/dive-go/token"
)

func ExampleSigner_Start() {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	signer, err := token.NewSigner(private, token.Issuer("https://app.example.com"))
	if err != nil {
		log.Fatal(err)
	}

	start, runID, err := signer.Start(token.StartClaims{
		Plan:   "triage",
		Tenant: "acme",
		User:   "ada@example.com",
		Extra:  map[string]any{"role": "admin"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(start)
	fmt.Println(len(runID))
	// Output:
	// [redacted]
	// 36
}

func ExampleNewVerifier() {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	signer, err := token.NewSigner(private, token.Issuer("https://app.example.com"))
	if err != nil {
		log.Fatal(err)
	}
	start, runID, err := signer.Start(token.StartClaims{Plan: "triage", Tenant: "acme", User: "ada@example.com"})
	if err != nil {
		log.Fatal(err)
	}

	verifier := token.NewVerifier(public, token.Issuer("https://app.example.com"))
	if err := verifier.Err(); err != nil {
		log.Fatal(err)
	}
	claims, err := verifier.Verify(string(start))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(claims.Tenant, claims.Subject, claims.Plan, claims.ID == runID)

	other := token.NewVerifier(public, token.Issuer("https://other.example.com"))
	_, err = other.Verify(string(start))
	fmt.Println(err)
	// Output:
	// acme ada@example.com triage true
	// invalid run token: the token's issuer isn't trusted
}
