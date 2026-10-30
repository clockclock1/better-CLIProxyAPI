package registry

import (
	"context"
	"os"
	"strings"
)

// defaultCommandCodeModelURLs returns the live Command Code provider model catalog endpoint.
func defaultCommandCodeModelURLs() []string {
	base := strings.TrimSpace(os.Getenv("COMMANDCODE_API_URL"))
	if base == "" {
		base = "https://api.commandcode.ai"
	}
	return []string{strings.TrimRight(base, "/") + "/provider/v1/models"}
}

// mutable so catalog fetcher can re-read env-backed defaults each configure.
var commandCodeModelsURLs = defaultCommandCodeModelURLs()

// StartCommandCodeModelsUpdater starts a background updater that fetches the
// Command Code model catalog immediately and refreshes it every 3 hours.
// Safe to call multiple times; only one updater runs.
func StartCommandCodeModelsUpdater(ctx context.Context) {
	commandCodeModelsURLs = defaultCommandCodeModelURLs()
	commandCodeCatalogUpdater.configure(ctx, "")
}

func validateCommandCodeCatalogBytes(data []byte) error {
	_, err := ValidateCommandCodeModelsJSON(data)
	return err
}
