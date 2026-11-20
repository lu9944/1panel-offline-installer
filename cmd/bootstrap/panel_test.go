package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

func TestEncryptPasswordMatchesPanelFormat(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	encrypted, err := encryptPassword("Passw0rd!", string(pemBytes))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(encrypted, ":")
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}
	keyCipher, _ := base64.StdEncoding.DecodeString(parts[0])
	iv, _ := base64.StdEncoding.DecodeString(parts[1])
	ciphertext, _ := base64.StdEncoding.DecodeString(parts[2])

	aesKeyHex, err := rsa.DecryptPKCS1v15(rand.Reader, key, keyCipher)
	if err != nil {
		t.Fatal(err)
	}
	if len(aesKeyHex) != 32 {
		t.Fatalf("unexpected AES key length: %d", len(aesKeyHex))
	}
	block, err := aes.NewCipher(aesKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	pad := int(plain[len(plain)-1])
	if pad <= 0 || pad > aes.BlockSize {
		t.Fatalf("invalid PKCS7 padding: %d", pad)
	}
	if string(plain[:len(plain)-pad]) != "Passw0rd!" {
		t.Fatalf("password mismatch: %q", plain[:len(plain)-pad])
	}
}

func TestBuildInstallParamsOverrides(t *testing.T) {
	app := &AppMeta{
		Key: "mysql",
		Params: []byte(`{"formFields":[
			{"envKey":"PANEL_DB_ROOT_PASSWORD","default":"mysql","type":"password"},
			{"envKey":"PANEL_APP_PORT_HTTP","default":3306,"type":"number"},
			{"envKey":"EXTRA","default":"keep","type":"text"}
		]}`),
	}
	opts := &installOptions{mysqlPassword: "secret", mysqlPort: 13306}
	params := buildInstallParams(app, opts)
	if params["PANEL_DB_ROOT_PASSWORD"] != "secret" {
		t.Fatalf("password override missing: %#v", params)
	}
	if params["PANEL_APP_PORT_HTTP"] != 13306 {
		t.Fatalf("port override missing: %#v", params)
	}
	if params["EXTRA"] != "keep" {
		t.Fatalf("default value missing: %#v", params)
	}
}

func TestRandomPassword(t *testing.T) {
	password := randomPassword(24)
	if len(password) != 24 {
		t.Fatalf("unexpected length: %d", len(password))
	}
	for _, r := range password {
		if !strings.ContainsRune("ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789", r) {
			t.Fatalf("unexpected character: %q", r)
		}
	}
}
