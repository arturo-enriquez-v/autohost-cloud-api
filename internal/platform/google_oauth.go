package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

// GoogleClaims contiene los datos verificados del ID token de Google
type GoogleClaims struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified string `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// VerifyGoogleIDToken verifica un Google ID token usando el endpoint tokeninfo de Google
func VerifyGoogleIDToken(idToken string) (*GoogleClaims, error) {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	if clientID == "" {
		return nil, errors.New("GOOGLE_CLIENT_ID not configured")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	params := url.Values{}
	params.Set("id_token", idToken)
	resp, err := client.Get("https://oauth2.googleapis.com/tokeninfo?" + params.Encode())
	if err != nil {
		return nil, fmt.Errorf("verify google token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("invalid google id_token")
	}

	var payload struct {
		GoogleClaims
		Aud string `json:"aud"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode google token: %w", err)
	}

	if payload.Aud != clientID {
		return nil, errors.New("token audience mismatch")
	}

	if payload.EmailVerified != "true" {
		return nil, errors.New("email not verified")
	}

	return &payload.GoogleClaims, nil
}
