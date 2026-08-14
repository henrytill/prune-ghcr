// Command prune-ghcr is the entrypoint for the action. It reads and validates
// inputs, constructs the clients, runs the prune, and reports the counts.
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/henrytill/prune-ghcr/internal/actions"
	"github.com/henrytill/prune-ghcr/internal/api"
	"github.com/henrytill/prune-ghcr/internal/prune"
	"github.com/henrytill/prune-ghcr/internal/registry"
)

// logger adapts the actions package to prune.Logger.
type logger struct{}

func (logger) Info(message string)  { actions.Info(message) }
func (logger) Error(message string) { actions.Error(message) }

func main() {
	if err := run(context.Background()); err != nil {
		actions.Error(err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	// A PAT pasted into a secret with a trailing newline makes for an invalid
	// Authorization header, and PATs contain no whitespace of their own. An
	// empty token is then a misconfiguration rather than an opt-out -- every
	// consuming repo is expected to hold a PAT -- so fail here instead of
	// leaving a green run that stopped pruning.
	token := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, actions.Input("token"))
	if token == "" {
		return fmt.Errorf("token input is empty (is the PAT secret set?)")
	}

	// Masks the stripped form too: the raw secret is already masked, but the
	// string actually sent differs from it if the secret had whitespace.
	actions.SetSecret(token)

	owner := actions.Input("owner")
	if owner == "" {
		return fmt.Errorf("owner input is empty")
	}
	packageName := actions.Input("package")
	if packageName == "" {
		return fmt.Errorf("package input is empty")
	}

	dryRun, err := actions.BoolInput("dry-run")
	if err != nil {
		return err
	}

	minAge, err := parseMinAge(actions.Input("min-age-hours"))
	if err != nil {
		return err
	}

	versions := api.NewClient(token, os.Getenv("GITHUB_API_URL"), actions.Warning)
	manifests, err := registry.NewClient(ctx, "", owner, packageName, token, actions.Warning)
	if err != nil {
		return err
	}

	result, err := prune.Prune(ctx, prune.Options{
		Owner:       owner,
		PackageName: packageName,
		MinAge:      minAge,
		DryRun:      dryRun,
	}, versions, manifests, logger{})
	if err != nil {
		return err
	}

	for name, value := range map[string]int{
		"deleted": result.Deleted,
		"kept":    result.Kept,
		"failed":  result.Failed,
	} {
		if err := actions.SetOutput(name, strconv.Itoa(value)); err != nil {
			return err
		}
	}

	if result.Failed > 0 {
		return fmt.Errorf("failed to delete %d version(s)", result.Failed)
	}
	return nil
}

// parseMinAge converts the min-age-hours input to a duration.
//
// An empty value is zero rather than an error, because the TypeScript version
// read this with Number(), where Number("") is 0. A caller passing an unset
// expression through this input is green today and must stay green.
func parseMinAge(input string) (time.Duration, error) {
	if input == "" {
		return 0, nil
	}
	// ParseFloat accepts "NaN" and "Inf", which Number.isFinite rejected.
	hours, err := strconv.ParseFloat(input, 64)
	if err != nil || math.IsNaN(hours) || math.IsInf(hours, 0) || hours < 0 {
		return 0, fmt.Errorf("min-age-hours must be a non-negative number, got '%s'", input)
	}
	return time.Duration(hours * float64(time.Hour)), nil
}
