package headscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewClient() *Client {
	baseURL := os.Getenv("HEADSCALE_URL")

	baseURL = strings.TrimRight(baseURL, "/")

	apiKey := os.Getenv("HEADSCALE_API_KEY")

	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) BaseURL() string {
	return c.baseURL
}

type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
}

type UserResponse struct {
	User User `json:"user"`
}

type ListUsersResponse struct {
	Users []User `json:"users"`
}

type CreateUserRequest struct {
	Name string `json:"name"`
}

type CreatePreAuthKeyRequest struct {
	User       string `json:"user"`
	Reusable   bool   `json:"reusable"`
	Expiration string `json:"expiration"`
}

type PreAuthKeyResponse struct {
	PreAuthKey struct {
		Key        string `json:"key"`
		Expiration string `json:"expiration"`
	} `json:"preAuthKey"`
	Key string `json:"key"`
}

// GetUserByName retrieves a Headscale user by name from the Headscale server.
func (c *Client) GetUserByName(ctx context.Context, username string) (*User, error) {
	url := fmt.Sprintf("%s/api/v1/user", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create list users request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute list users request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list users failed (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	var res ListUsersResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("decode list users response: %w", err)
	}

	for _, u := range res.Users {
		if strings.EqualFold(u.Name, username) {
			return &u, nil
		}
	}

	return nil, fmt.Errorf("user %q not found", username)
}

// EnsureUser creates a user in Headscale if it does not exist, and returns the user object.
func (c *Client) EnsureUser(ctx context.Context, username string) (*User, error) {
	url := fmt.Sprintf("%s/api/v1/user", c.baseURL)
	reqBody, err := json.Marshal(CreateUserRequest{Name: username})
	if err != nil {
		return nil, fmt.Errorf("marshal user request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create user request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute user request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		var res UserResponse
		if err := json.Unmarshal(bodyBytes, &res); err == nil && res.User.ID != "" {
			return &res.User, nil
		}
	}

	// Fallback if user already exists
	u, err := c.GetUserByName(ctx, username)
	if err == nil {
		return u, nil
	}

	return nil, fmt.Errorf("headscale user create failed (%d): %s", resp.StatusCode, string(bodyBytes))
}

// CreatePreAuthKey generates a pre-authentication key for a given Headscale user ID.
// Expiration is formatted as an RFC3339 timestamp as required by Headscale protobuf API.
func (c *Client) CreatePreAuthKey(ctx context.Context, userID string, reusable bool, duration time.Duration) (string, error) {
	if duration <= 0 {
		duration = 720 * time.Hour
	}

	expirationTS := time.Now().Add(duration).Format(time.RFC3339)

	url := fmt.Sprintf("%s/api/v1/preauthkey", c.baseURL)
	reqBody, err := json.Marshal(CreatePreAuthKeyRequest{
		User:       userID,
		Reusable:   reusable,
		Expiration: expirationTS,
	})
	if err != nil {
		return "", fmt.Errorf("marshal preauthkey request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return "", fmt.Errorf("create preauthkey request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute preauthkey request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read preauthkey response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("headscale preauthkey failed (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	var res PreAuthKeyResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return "", fmt.Errorf("unmarshal preauthkey response: %w", err)
	}

	key := res.PreAuthKey.Key
	if key == "" {
		key = res.Key
	}
	if key == "" {
		return "", fmt.Errorf("preauthkey not found in response: %s", string(bodyBytes))
	}

	return key, nil
}
