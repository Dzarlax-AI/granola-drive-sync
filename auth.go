package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
)

// RunAuth performs the one-time OAuth2 authorization flow and prints the refresh token.
func RunAuth(clientID, clientSecret string) {
	cfg := oauthConfig(clientID, clientSecret, "")

	// Start a local server to catch the redirect
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURL := fmt.Sprintf("http://localhost:%d", port)
	cfg.RedirectURL = redirectURL

	codeCh := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		fmt.Fprintln(w, "<h2>Done! You can close this tab.</h2>")
		codeCh <- code
	})}
	go srv.Serve(listener) //nolint:errcheck

	authURL := cfg.AuthCodeURL("state", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Println("Opening browser for Google authorization...")
	openBrowser(authURL)
	fmt.Println("Waiting for authorization...")

	code := <-codeCh
	srv.Shutdown(context.Background()) //nolint:errcheck

	token, err := cfg.Exchange(context.Background(), code)
	if err != nil {
		log.Fatalf("token exchange: %v", err)
	}

	fmt.Println("\n✓ Authorization successful!")
	fmt.Println("\nAdd this to your .env file:")
	fmt.Printf("GOOGLE_REFRESH_TOKEN=%s\n", token.RefreshToken)
}

func oauthConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{drive.DriveScope},
		Endpoint:     google.Endpoint,
	}
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "linux":
		cmd, args = "xdg-open", []string{url}
	default:
		fmt.Printf("Open this URL in your browser:\n%s\n", url)
		return
	}
	if err := exec.Command(cmd, args...).Start(); err != nil {
		fmt.Printf("Open this URL in your browser:\n%s\n", url)
	}
}
