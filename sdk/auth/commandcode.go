package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	commandcodeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// CommandCodeAuthenticator implements browser OAuth for Command Code (studio/auth/cli).
type CommandCodeAuthenticator struct {
	CallbackPort int
	StudioBase   string
	APIBase      string
}

// NewCommandCodeAuthenticator constructs a Command Code authenticator.
func NewCommandCodeAuthenticator() *CommandCodeAuthenticator {
	return &CommandCodeAuthenticator{CallbackPort: 0}
}

// Provider returns the provider key for Command Code.
func (a *CommandCodeAuthenticator) Provider() string {
	return "commandcode"
}

// RefreshLead disables scheduled refresh; Command Code API keys do not expire via OAuth refresh.
func (a *CommandCodeAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login launches the browser OAuth flow and returns a persisted auth record.
func (a *CommandCodeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	studioBase := strings.TrimSpace(a.StudioBase)
	if studioBase == "" {
		studioBase = strings.TrimSpace(os.Getenv("COMMANDCODE_STUDIO_URL"))
	}
	if studioBase == "" {
		studioBase = commandcodeauth.DefaultStudioBaseURL
	}
	apiBase := strings.TrimSpace(a.APIBase)
	if apiBase == "" {
		apiBase = strings.TrimSpace(os.Getenv("COMMANDCODE_API_URL"))
	}
	if apiBase == "" {
		apiBase = commandcodeauth.DefaultAPIBaseURL
	}

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		return nil, fmt.Errorf("commandcode state generation failed: %w", errState)
	}

	if opts.NoBrowser {
		if opts.Prompt == nil {
			return nil, fmt.Errorf("commandcode authentication in no-browser mode requires an interactive prompt")
		}
		callbackPort := a.CallbackPort
		if opts.CallbackPort > 0 {
			callbackPort = opts.CallbackPort
		}
		oauthServer := commandcodeauth.NewOAuthServer(callbackPort, state)
		actualPort, errStart := oauthServer.Start()
		if errStart != nil {
			return nil, fmt.Errorf("failed to start commandcode oauth callback server: %w", errStart)
		}
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = oauthServer.Stop(stopCtx)
		}()
		callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", actualPort)
		authURL := commandcodeauth.BuildAuthURL(studioBase, callbackURL, state, "cli")
		fmt.Printf("Visit the following URL to continue Command Code authentication:\n%s\n\n", authURL)
		util.PrintSSHTunnelInstructions(actualPort)
		fmt.Println("Waiting for authorization callback...")
		result, errWait := oauthServer.WaitForCallbackWithContext(ctx, 5*time.Minute)
		if errWait != nil {
			return nil, fmt.Errorf("commandcode: %w", errWait)
		}
		if result.Error != "" {
			msg := result.Error
			if result.ErrorDesc != "" {
				msg = result.ErrorDesc
			}
			return nil, fmt.Errorf("commandcode oauth error: %s", msg)
		}
		return commandcodeauth.CreateAuthRecord(result, apiBase)
	}

	callbackPort := a.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}
	oauthServer := commandcodeauth.NewOAuthServer(callbackPort, state)
	actualPort, errStart := oauthServer.Start()
	if errStart != nil {
		return nil, fmt.Errorf("failed to start commandcode oauth callback server: %w", errStart)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = oauthServer.Stop(stopCtx)
	}()

	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", actualPort)
	authURL := commandcodeauth.BuildAuthURL(studioBase, callbackURL, state, "cli")

	fmt.Println("Starting Command Code authentication...")
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", authURL)

	if !browser.IsAvailable() {
		log.Warn("No browser available; please open the URL manually")
		util.PrintSSHTunnelInstructions(actualPort)
	} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
		log.Warnf("Failed to open browser automatically: %v", errOpen)
		util.PrintSSHTunnelInstructions(actualPort)
	} else {
		fmt.Println("Browser opened automatically.")
	}

	fmt.Println("Waiting for authorization...")
	result, errWait := oauthServer.WaitForCallbackWithContext(ctx, 5*time.Minute)
	if errWait != nil {
		return nil, fmt.Errorf("commandcode: %w", errWait)
	}
	if result.Error != "" {
		msg := result.Error
		if result.ErrorDesc != "" {
			msg = result.ErrorDesc
		}
		return nil, fmt.Errorf("commandcode oauth error: %s", msg)
	}

	record, errRecord := commandcodeauth.CreateAuthRecord(result, apiBase)
	if errRecord != nil {
		return nil, errRecord
	}
	fmt.Println("Command Code authentication successful")
	return record, nil
}
