// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package secretbox

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func testBox(t *testing.T) *Box {
	t.Helper()
	b, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpenRoundTrip(t *testing.T) {
	b := testBox(t)
	sealed, err := b.Seal("s3cret-token", "connector-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "s3cret") {
		t.Fatalf("sealed value leaks the plaintext: %q", sealed)
	}
	got, err := b.Open(sealed, "connector-1")
	if err != nil || got != "s3cret-token" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestSealUsesFreshNonce(t *testing.T) {
	b := testBox(t)
	a, _ := b.Seal("same", "id")
	c, _ := b.Seal("same", "id")
	if a == c {
		t.Fatal("two seals of the same plaintext must differ")
	}
}

func TestOpenRejectsOtherRecord(t *testing.T) {
	b := testBox(t)
	sealed, _ := b.Seal("token", "connector-1")
	if _, err := b.Open(sealed, "connector-2"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Open with other associated data = %v, want ErrDecrypt", err)
	}
}

func TestOpenRejectsOtherKeyAndGarbage(t *testing.T) {
	sealed, _ := testBox(t).Seal("token", "id")
	other, _ := New(bytes.Repeat([]byte{8}, 32))
	for name, in := range map[string]string{"other key": sealed, "no prefix": "abc", "bad base64": "v1:***", "short": "v1:AAAA"} {
		box := testBox(t)
		if name == "other key" {
			box = other
		}
		if _, err := box.Open(in, "id"); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: err = %v, want ErrDecrypt", name, err)
		}
	}
}

func TestNilBoxNotConfigured(t *testing.T) {
	var b *Box
	if b.Configured() {
		t.Fatal("nil box reports configured")
	}
	if _, err := b.Seal("x", "id"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Seal = %v", err)
	}
	if _, err := b.Open("v1:x", "id"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Open = %v", err)
	}
}

func TestParseKey(t *testing.T) {
	key := bytes.Repeat([]byte{0xab}, 32)
	for _, s := range []string{hex.EncodeToString(key), base64.StdEncoding.EncodeToString(key), base64.RawURLEncoding.EncodeToString(key)} {
		got, err := ParseKey(" " + s + "\n")
		if err != nil || !bytes.Equal(got, key) {
			t.Errorf("ParseKey(%q) = %x, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "short", hex.EncodeToString(key[:16]), base64.StdEncoding.EncodeToString(key[:31])} {
		if _, err := ParseKey(s); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("ParseKey(%q) err = %v, want ErrInvalidKey", s, err)
		}
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvKey, "")
	if b, err := FromEnv(); b != nil || err != nil {
		t.Fatalf("empty env: %v, %v", b, err)
	}
	t.Setenv(EnvKey, "not-a-key")
	if _, err := FromEnv(); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("invalid env: %v", err)
	}
	t.Setenv(EnvKey, hex.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if b, err := FromEnv(); err != nil || !b.Configured() {
		t.Fatalf("valid env: %v, %v", b, err)
	}
}
