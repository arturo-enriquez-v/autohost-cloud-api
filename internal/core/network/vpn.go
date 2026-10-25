// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package network

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/auth"
	"github.com/arturo/autohost-cloud-api/internal/platform/headscale"
)

type VPNKeyResponse struct {
	LoginServer string `json:"login_server"`
	PreAuthKey  string `json:"preauth_key"`
	User        string `json:"user"`
	Command     string `json:"command"`
}

type VPNService struct {
	client   *headscale.Client
	authRepo auth.Repository
}

func NewVPNService(client *headscale.Client, authRepo auth.Repository) *VPNService {
	return &VPNService{
		client:   client,
		authRepo: authRepo,
	}
}

var (
	sanitizeRegex  = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	multiHyphenReg = regexp.MustCompile(`-+`)
)

func FormatHeadscaleUsername(name string, userID string) string {
	cleanName := strings.TrimSpace(strings.ToLower(name))
	cleanName = sanitizeRegex.ReplaceAllString(cleanName, "-")
	cleanName = multiHyphenReg.ReplaceAllString(cleanName, "-")
	cleanName = strings.Trim(cleanName, "-")

	if cleanName == "" {
		cleanName = "user"
	}

	shortID := userID
	if len(userID) > 8 {
		shortID = userID[:8]
	}

	return fmt.Sprintf("%s-%s", cleanName, shortID)
}

func (s *VPNService) GenerateVPNKey(ctx context.Context, userID string) (*VPNKeyResponse, error) {
	userObj, err := s.authRepo.FindUserByID(userID)
	if err != nil {
		return nil, fmt.Errorf("find user: %w", err)
	}

	userName := ""
	if userObj.Name != nil {
		userName = *userObj.Name
	}
	if userName == "" {
		userName = userObj.Email
	}

	hsUsername := FormatHeadscaleUsername(userName, userObj.ID)

	hsUser, err := s.client.EnsureUser(ctx, hsUsername)
	if err != nil {
		return nil, fmt.Errorf("ensure headscale user: %w", err)
	}

	preauthKey, err := s.client.CreatePreAuthKey(ctx, hsUser.ID, true, 720*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("generate headscale preauthkey: %w", err)
	}

	loginServer := s.client.BaseURL()
	var command string
	if loginServer == "https://hs.autohst.dev" || loginServer == "" {
		command = fmt.Sprintf("autohost vpn connect --authkey %s", preauthKey)
	} else {
		command = fmt.Sprintf("autohost vpn connect --login-server %s --authkey %s", loginServer, preauthKey)
	}

	return &VPNKeyResponse{
		LoginServer: loginServer,
		PreAuthKey:  preauthKey,
		User:        hsUsername,
		Command:     command,
	}, nil
}
