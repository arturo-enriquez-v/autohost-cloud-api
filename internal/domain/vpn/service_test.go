package vpn

import (
	"testing"
)

func TestFormatHeadscaleUsername(t *testing.T) {
	tests := []struct {
		name     string
		userID   string
		expected string
	}{
		{
			name:     "John Doe",
			userID:   "a1b2c3d4-e5f6-7890",
			expected: "john-doe-a1b2c3d4",
		},
		{
			name:     "   ",
			userID:   "123456789",
			expected: "user-12345678",
		},
		{
			name:     "Special!@#$%^&*()Name",
			userID:   "abcdef123456",
			expected: "special-name-abcdef12",
		},
	}

	for _, tt := range tests {
		result := FormatHeadscaleUsername(tt.name, tt.userID)
		if result != tt.expected {
			t.Errorf("FormatHeadscaleUsername(%q, %q) = %q; want %q", tt.name, tt.userID, result, tt.expected)
		}
	}
}
