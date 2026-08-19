// Command prune-ghcr is the entrypoint for the action. It reads and validates
// inputs, constructs the clients, runs the prune, and reports the counts.
package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/henrytill/prune-ghcr/internal/actions"
	"github.com/henrytill/prune-ghcr/internal/api"
	"github.com/henrytill/prune-ghcr/internal/prune"
	"github.com/henrytill/prune-ghcr/internal/registry"
)

// logger adapts the actions package to prune.Logger.
type logger struct{}

func (logger) Info(message string)    { actions.Info(message) }
func (logger) Warning(message string) { actions.Warning(message) }
func (logger) Error(message string)   { actions.Error(message) }

func main() {
	if err := run(context.Background()); err != nil {
		actions.Error(err.Error())
		os.Exit(1)
	}
}

// inputs is what the action was invoked with, once validated.
type inputs struct {
	token   string
	options prune.Options
}

// readInputs reads and validates the action's inputs.
func readInputs() (inputs, error) {
	// A PAT pasted into a secret with a trailing newline makes for an invalid
	// Authorization header, and PATs contain no whitespace of their own. An
	// empty token is then a misconfiguration rather than an opt-out -- every
	// consuming repo is expected to hold a PAT -- so fail here instead of
	// leaving a green run that stopped pruning.
	token := strings.Join(strings.Fields(actions.Input("token")), "")
	if token == "" {
		return inputs{}, errors.New("token input is empty (is the PAT secret set?)")
	}

	owner := actions.Input("owner")
	if owner == "" {
		return inputs{}, errors.New("owner input is empty")
	}
	packageName := actions.Input("package")
	if packageName == "" {
		return inputs{}, errors.New("package input is empty")
	}

	dryRun, err := actions.BoolInput("dry-run")
	if err != nil {
		return inputs{}, err
	}

	minAge, err := parseMinAge(actions.Input("min-age-hours"))
	if err != nil {
		return inputs{}, err
	}

	return inputs{
		token: token,
		options: prune.Options{
			Owner:       owner,
			PackageName: packageName,
			MinAge:      minAge,
			DryRun:      dryRun,
		},
	}, nil
}

// pruneAndReport prunes and reports the counts as the action's outputs.
//
// Separate from run so that it can be driven with fakes: run constructs clients
// against ghcr.io and the packages API, which a test cannot reach.
func pruneAndReport(
	ctx context.Context,
	options prune.Options,
	versions prune.Versions,
	manifests prune.ManifestReader,
	log prune.Logger,
) error {
	result, err := prune.Prune(ctx, options, versions, manifests, log)
	if err != nil {
		return err
	}

	// total is what makes a dry run legible: it reports deleted and failed as
	// zero whatever it found, so total minus kept is the only thing that says
	// how much a real run would have removed.
	//
	// Written before the failure below is returned, not instead of it: a run
	// that failed some deletes still made the others, and a workflow step
	// reading `deleted` after a failure gets the number that happened.
	if err := errors.Join(
		actions.SetOutput("total", strconv.Itoa(result.Total)),
		actions.SetOutput("deleted", strconv.Itoa(result.Deleted)),
		actions.SetOutput("kept", strconv.Itoa(result.Kept)),
		actions.SetOutput("failed", strconv.Itoa(result.Failed)),
	); err != nil {
		return err
	}

	if result.Failed > 0 {
		return fmt.Errorf("failed to delete %d version(s)", result.Failed)
	}
	return nil
}

func run(ctx context.Context) error {
	in, err := readInputs()
	if err != nil {
		return err
	}

	// Masks the stripped form too: the raw secret is already masked, but the
	// string actually sent differs from it if the secret had whitespace.
	actions.SetSecret(in.token)

	versions, err := api.NewClient(in.token, os.Getenv("GITHUB_API_URL"), actions.Warning)
	if err != nil {
		return err
	}
	manifests, err := registry.NewClient(
		in.options.Owner, in.options.PackageName, in.token, actions.Warning)
	if err != nil {
		return err
	}

	return pruneAndReport(ctx, in.options, versions, manifests, logger{})
}

// parseMinAge converts the min-age-hours input to a duration.
//
// An empty value is zero rather than an error: an unset expression reaching this
// input has always been accepted, and a caller whose workflow passes one is
// green today and must stay green.
func parseMinAge(input string) (time.Duration, error) {
	if input == "" {
		return 0, nil
	}
	// ParseFloat accepts "NaN" and "Inf", neither of which is an age.
	hours, err := strconv.ParseFloat(input, 64)
	if err != nil || math.IsNaN(hours) || math.IsInf(hours, 0) || hours < 0 {
		return 0, fmt.Errorf("min-age-hours must be a non-negative number, got '%s'", input)
	}
	// Beyond this the conversion below overflows to a negative duration, and a
	// negative min-age skips every version as younger than a cutoff centuries
	// in the past -- a silent no-op where the user asked for the opposite.
	const maxHours = math.MaxInt64 / int64(time.Hour)
	if hours > float64(maxHours) {
		return 0, fmt.Errorf("min-age-hours must be at most %d, got '%s'", maxHours, input)
	}
	return time.Duration(hours * float64(time.Hour)), nil
}
