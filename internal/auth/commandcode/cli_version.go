package commandcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultCLIVersionPollInterval = 3 * time.Hour
	defaultCLIVersionFetchTimeout = 15 * time.Second
	defaultNPMLatestURL           = "https://registry.npmjs.org/command-code/latest"
)

var (
	cliVersionValue   atomic.Value // string
	cliVersionOnce    sync.Once
	cliVersionFetchMu sync.Mutex
)

func init() {
	cliVersionValue.Store(DefaultCLIVersion)
}

// CurrentCLIVersion returns the auto-updated Command Code CLI version used for
// x-command-code-version when no explicit override is set.
func CurrentCLIVersion() string {
	if v, ok := cliVersionValue.Load().(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return DefaultCLIVersion
}

// HeaderCLIVersion resolves the version header for a request.
// Priority: COMMANDCODE_CLI_VERSION env > auth cli_version attribute > auto-updated current.
func HeaderCLIVersion(authAttributes map[string]string) string {
	ensureCLIVersionUpdaterStarted()
	if v := strings.TrimSpace(os.Getenv("COMMANDCODE_CLI_VERSION")); v != "" {
		return v
	}
	if authAttributes != nil {
		if v := strings.TrimSpace(authAttributes["cli_version"]); v != "" {
			return v
		}
	}
	return CurrentCLIVersion()
}

// StartCLIVersionUpdater fetches the latest npm version immediately and refreshes
// on an interval. Safe to call multiple times; only one loop runs.
func StartCLIVersionUpdater(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	cliVersionOnce.Do(func() {
		go runCLIVersionUpdater(ctx)
	})
}

func ensureCLIVersionUpdaterStarted() {
	StartCLIVersionUpdater(context.Background())
}

func runCLIVersionUpdater(ctx context.Context) {
	refreshCLIVersion(ctx)
	ticker := time.NewTicker(cliVersionPollInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshCLIVersion(ctx)
		}
	}
}

func cliVersionPollInterval() time.Duration {
	if v := strings.TrimSpace(os.Getenv("COMMANDCODE_CLI_VERSION_POLL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultCLIVersionPollInterval
}

func cliVersionLatestURL() string {
	if v := strings.TrimSpace(os.Getenv("COMMANDCODE_CLI_VERSION_URL")); v != "" {
		return v
	}
	return defaultNPMLatestURL
}

func refreshCLIVersion(ctx context.Context) {
	cliVersionFetchMu.Lock()
	defer cliVersionFetchMu.Unlock()

	version, err := fetchLatestCLIVersion(ctx)
	if err != nil {
		log.Debugf("commandcode: cli version refresh failed: %v", err)
		return
	}
	if noteCLIVersionLocked(version) {
		log.Infof("commandcode: cli version updated to %s", CurrentCLIVersion())
	}
}

func fetchLatestCLIVersion(ctx context.Context) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, defaultCLIVersionFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, cliVersionLatestURL(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI-commandcode-version")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("npm latest HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	version := strings.TrimSpace(payload.Version)
	if !isValidSemver(version) {
		return "", fmt.Errorf("invalid npm version %q", version)
	}
	return version, nil
}

// NoteMinVersion raises the stored CLI version when the server reports a higher
// minimum (upgrade_required). Returns true when the stored value changed.
func NoteMinVersion(minVersion string) bool {
	minVersion = strings.TrimSpace(minVersion)
	if !isValidSemver(minVersion) {
		return false
	}
	cliVersionFetchMu.Lock()
	defer cliVersionFetchMu.Unlock()
	return noteCLIVersionLocked(minVersion)
}

// NoteUpgradeRequired parses an upgrade_required JSON body, raises the floor,
// and synchronously refreshes from npm so a retry can use the latest version.
func NoteUpgradeRequired(body []byte) (changed bool, minVersion string) {
	before := CurrentCLIVersion()
	minVersion = parseUpgradeMinVersion(body)
	if minVersion != "" {
		_ = NoteMinVersion(minVersion)
	}
	// Server minVersion is often far below latest; pull npm latest before retry.
	refreshCLIVersion(context.Background())
	return CurrentCLIVersion() != before, minVersion
}

func parseUpgradeMinVersion(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload struct {
		Error struct {
			Code       string `json:"code"`
			MinVersion string `json:"minVersion"`
			Message    string `json:"message"`
		} `json:"error"`
		Code       string `json:"code"`
		MinVersion string `json:"minVersion"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	code := strings.TrimSpace(payload.Error.Code)
	if code == "" {
		code = strings.TrimSpace(payload.Code)
	}
	if !strings.EqualFold(code, "upgrade_required") {
		// Still accept nested minVersion when message mentions upgrade.
		msg := strings.ToLower(payload.Error.Message)
		if !strings.Contains(msg, "out of date") && !strings.Contains(msg, "upgrade") {
			return ""
		}
	}
	minVersion := strings.TrimSpace(payload.Error.MinVersion)
	if minVersion == "" {
		minVersion = strings.TrimSpace(payload.MinVersion)
	}
	if !isValidSemver(minVersion) {
		return ""
	}
	return minVersion
}

func noteCLIVersionLocked(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if !isValidSemver(candidate) {
		return false
	}
	current := CurrentCLIVersion()
	if compareSemver(candidate, current) <= 0 {
		return false
	}
	cliVersionValue.Store(candidate)
	return true
}

func isValidSemver(v string) bool {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for i, part := range parts {
		if i == 3 {
			// allow build/prerelease suffix on last segment loosely
			part = strings.SplitN(part, "-", 2)[0]
			part = strings.SplitN(part, "+", 2)[0]
		} else if i == len(parts)-1 {
			part = strings.SplitN(part, "-", 2)[0]
			part = strings.SplitN(part, "+", 2)[0]
		}
		if part == "" {
			return false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return false
		}
	}
	return true
}

// compareSemver returns -1/0/1 for a<b / a==b / a>b. Invalid versions compare as equal to empty.
func compareSemver(a, b string) int {
	ap := semverParts(a)
	bp := semverParts(b)
	for i := 0; i < 3; i++ {
		if ap[i] < bp[i] {
			return -1
		}
		if ap[i] > bp[i] {
			return 1
		}
	}
	return 0
}

func semverParts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}

// ResetCLIVersionForTest restores the bootstrap default (tests only).
func ResetCLIVersionForTest() {
	cliVersionValue.Store(DefaultCLIVersion)
}
