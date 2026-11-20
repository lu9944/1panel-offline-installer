package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

type panelClient struct {
	baseURL  string
	entrance string
	http     *http.Client
	jar      *cookiejar.Jar
}

type apiResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func newPanelClient(baseURL, entrance string, insecure bool) (*panelClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid panel URL: %q", baseURL)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecure},
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &panelClient{
		baseURL:  baseURL,
		entrance: entrance,
		jar:      jar,
		http: &http.Client{
			Transport: transport,
			Jar:       jar,
			Timeout:   60 * time.Second,
		},
	}, nil
}

func (c *panelClient) url(path string) string {
	return c.baseURL + "/api/v2/" + strings.TrimLeft(path, "/")
}

func (c *panelClient) cookie(name string) string {
	parsed, _ := url.Parse(c.baseURL)
	if parsed == nil {
		return ""
	}
	for _, ck := range c.jar.Cookies(parsed) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

func (c *panelClient) request(method, path string, payload interface{}, extra map[string]string) ([]byte, int, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = strings.NewReader(string(data))
	}
	req, err := http.NewRequest(method, c.url(path), body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "zh")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("CurrentNode", "local")
	req.Header.Set("User-Agent", "1panel-offline-installer")
	if method != http.MethodGet {
		if csrf := c.cookie("pcsrftoken"); csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func parseAPIResponse(data []byte) (*apiResponse, error) {
	var res apiResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("invalid panel response: %w (%s)", err, truncate(string(data), 300))
	}
	return &res, nil
}

func (c *panelClient) waitHTTP(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		data, code, err := c.request(http.MethodGet, "core/auth/setting", nil, nil)
		if err == nil && code == http.StatusOK {
			res, err := parseAPIResponse(data)
			if err == nil && res.Code == 200 {
				return nil
			}
			last = truncate(string(data), 200)
		} else if err != nil {
			last = err.Error()
		} else {
			last = fmt.Sprintf("HTTP %d: %s", code, truncate(string(data), 200))
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("panel did not become ready: %s", last)
}

func (c *panelClient) publicKey() (string, error) {
	// The public-key cookie is set by /core/auth/setting.
	data, code, err := c.request(http.MethodGet, "core/auth/setting", nil, nil)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("get panel setting: HTTP %d: %s", code, truncate(string(data), 200))
	}
	raw := c.cookie("panel_public_key")
	if raw == "" {
		return "", errors.New("panel did not provide panel_public_key cookie")
	}
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		return "", fmt.Errorf("decode panel public key cookie: %w", err)
	}
	pemBytes, err := base64.StdEncoding.DecodeString(decoded)
	if err != nil {
		return "", fmt.Errorf("decode panel public key: %w", err)
	}
	return string(pemBytes), nil
}

func (c *panelClient) login(username, password string) error {
	pub, err := c.publicKey()
	if err != nil {
		return err
	}
	encrypted, err := encryptPassword(password, pub)
	if err != nil {
		return err
	}
	body := map[string]interface{}{
		"name":       username,
		"password":   encrypted,
		"captcha":    "",
		"captchaID":  "",
		"language":   "zh",
		"authSource": "local",
	}
	headers := map[string]string{}
	if c.entrance != "" {
		headers["EntranceCode"] = base64.StdEncoding.EncodeToString([]byte(c.entrance))
	}
	data, code, err := c.request(http.MethodPost, "core/auth/login", body, headers)
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	res, err := parseAPIResponse(data)
	if err != nil {
		return err
	}
	if code != http.StatusOK || res.Code != 200 {
		return fmt.Errorf("login failed (HTTP %d, code %d): %s", code, res.Code, truncate(res.Message, 300))
	}
	var loginData struct {
		MfaStatus string `json:"mfaStatus"`
	}
	if len(res.Data) > 0 {
		_ = json.Unmarshal(res.Data, &loginData)
	}
	if strings.EqualFold(loginData.MfaStatus, "Enable") {
		return errors.New("panel account has MFA enabled; offline automation cannot log in")
	}
	return nil
}

func (c *panelClient) waitAgent(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		data, code, err := c.request(http.MethodGet, "apps/installed/list", nil, nil)
		if err == nil && code == http.StatusOK {
			res, err := parseAPIResponse(data)
			if err == nil && res.Code == 200 {
				return nil
			}
			last = truncate(string(data), 200)
		} else if err != nil {
			last = err.Error()
		} else {
			last = fmt.Sprintf("HTTP %d: %s", code, truncate(string(data), 200))
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("panel agent did not become ready: %s", last)
}

func encryptPassword(password, publicKeyPEM string) (string, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return "", errors.New("invalid panel public key PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse panel public key: %w", err)
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return "", errors.New("panel public key is not RSA")
	}

	aesRaw := make([]byte, 16)
	if _, err := rand.Read(aesRaw); err != nil {
		return "", err
	}
	aesKeyHex := hex.EncodeToString(aesRaw) // 32 ASCII bytes -> AES-256
	keyCipher, err := rsa.EncryptPKCS1v15(rand.Reader, pub, []byte(aesKeyHex))
	if err != nil {
		return "", fmt.Errorf("encrypt AES key: %w", err)
	}

	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	blockCipher, err := aes.NewCipher([]byte(aesKeyHex))
	if err != nil {
		return "", err
	}
	plain := []byte(password)
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(plain, bytesRepeat(byte(pad), pad)...)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(blockCipher, iv).CryptBlocks(ciphertext, padded)

	return fmt.Sprintf("%s:%s:%s",
		base64.StdEncoding.EncodeToString(keyCipher),
		base64.StdEncoding.EncodeToString(iv),
		base64.StdEncoding.EncodeToString(ciphertext),
	), nil
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}
