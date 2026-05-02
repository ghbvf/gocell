package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	devicecell "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
)

func main() {
	subject := flag.String("subject", "iotdevice-local-admin", "JWT subject")
	roles := flag.String("roles", strings.Join(defaultRoles(), ","), "comma-separated roles")
	ttl := flag.Duration("ttl", 8*time.Hour, "token TTL")
	flag.Parse()

	token, err := issueToken(*subject, *roles, *ttl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "localtoken: %v\n", err)
		os.Exit(1)
	}
	if _, err := fmt.Fprintln(os.Stdout, token); err != nil {
		fmt.Fprintf(os.Stderr, "localtoken: write output: %v\n", err)
		os.Exit(1)
	}
}

func defaultRoles() []string {
	return []string{
		devicecell.RoleAdmin,
		devicecell.RoleOperator,
		devicecell.RoleDevice,
	}
}

func issueToken(subject string, rolesCSV string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", fmt.Errorf("ttl must be positive")
	}
	issuerName := strings.TrimSpace(os.Getenv("GOCELL_JWT_ISSUER"))
	if issuerName == "" {
		return "", fmt.Errorf("GOCELL_JWT_ISSUER must be set")
	}
	audience := strings.TrimSpace(os.Getenv("GOCELL_JWT_AUDIENCE"))
	if audience == "" {
		return "", fmt.Errorf("GOCELL_JWT_AUDIENCE must be set")
	}
	keySet, err := auth.LoadKeySetFromEnv(clock.Real())
	if err != nil {
		return "", fmt.Errorf("load JWT key set from environment: %w", err)
	}
	issuer, err := auth.NewJWTIssuer(keySet, issuerName, ttl, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{audience}))
	if err != nil {
		return "", fmt.Errorf("create JWT issuer: %w", err)
	}
	return issuer.Issue(auth.TokenIntentAccess, subject, auth.IssueOptions{
		Roles:    parseRoles(rolesCSV),
		Audience: []string{audience},
	})
}

func parseRoles(raw string) []string {
	parts := strings.Split(raw, ",")
	roles := make([]string, 0, len(parts))
	for _, part := range parts {
		if role := strings.TrimSpace(part); role != "" {
			roles = append(roles, role)
		}
	}
	return roles
}
