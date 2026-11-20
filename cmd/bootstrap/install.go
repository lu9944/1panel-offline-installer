package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	statusRunning    = "Running"
	statusInstalling = "Installing"
)

type installOptions struct {
	baseDir           string
	metaPath          string
	panelURL          string
	entrance          string
	username          string
	password          string
	mysqlPassword     string
	redisPassword     string
	mysqlPort         int
	redisPort         int
	openrestyHTTPPort int
	openrestyHTTPS    int
	timeout           time.Duration
	insecure          bool
	dryRun            bool
	allowExternal     bool
	onlyApps          map[string]bool
}

func runInstall(args []string) int {
	fsys := flag.NewFlagSet("install", flag.ExitOnError)
	opts := installOptions{}
	fsys.StringVar(&opts.baseDir, "base-dir", "/opt", "1Panel base directory")
	fsys.StringVar(&opts.metaPath, "meta", "", "path to meta/apps.json (required)")
	fsys.StringVar(&opts.panelURL, "panel-url", "http://127.0.0.1:22120", "panel base URL")
	fsys.StringVar(&opts.entrance, "entrance", "", "panel security entrance")
	fsys.StringVar(&opts.username, "username", "admin", "panel username")
	fsys.StringVar(&opts.password, "password", "", "panel password (required)")
	fsys.StringVar(&opts.mysqlPassword, "mysql-password", "", "MySQL root password (default: random)")
	fsys.StringVar(&opts.redisPassword, "redis-password", "", "Redis password (default: random)")
	fsys.IntVar(&opts.mysqlPort, "mysql-port", 3306, "MySQL host port")
	fsys.IntVar(&opts.redisPort, "redis-port", 6379, "Redis host port")
	fsys.IntVar(&opts.openrestyHTTPPort, "openresty-http-port", 80, "OpenResty HTTP port")
	fsys.IntVar(&opts.openrestyHTTPS, "openresty-https-port", 443, "OpenResty HTTPS port")
	fsys.DurationVar(&opts.timeout, "timeout", 20*time.Minute, "maximum time to wait for each app")
	fsys.BoolVar(&opts.insecure, "insecure", false, "skip TLS verification")
	fsys.BoolVar(&opts.dryRun, "dry-run", false, "print install requests without executing them")
	fsys.BoolVar(&opts.allowExternal, "allow-external-ports", false, "bind MySQL/Redis ports on all interfaces")
	only := fsys.String("apps", "", "comma-separated app keys to install (default: all bundled)")
	if err := fsys.Parse(args); err != nil {
		return 2
	}
	if opts.metaPath == "" || opts.password == "" {
		fmt.Fprintln(os.Stderr, "install: --meta and --password are required")
		return 2
	}
	if only != nil && *only != "" {
		opts.onlyApps = map[string]bool{}
		for _, key := range strings.Split(*only, ",") {
			opts.onlyApps[strings.TrimSpace(key)] = true
		}
	}

	meta, err := loadBundleMeta(opts.metaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}
	if opts.mysqlPassword == "" {
		opts.mysqlPassword = randomPassword(24)
	}
	if opts.redisPassword == "" {
		opts.redisPassword = randomPassword(24)
	}

	db, err := openAgentDBReadOnly(opts.baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}
	defer db.Close()

	client, err := newPanelClient(opts.panelURL, opts.entrance, opts.insecure)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}
	if !opts.dryRun {
		fmt.Println("==> 等待 1Panel 服务就绪...")
		if err := client.waitHTTP(2 * time.Minute); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			return 1
		}
		fmt.Println("==> 登录 1Panel...")
		if err := client.login(opts.username, opts.password); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			return 1
		}
		if err := client.waitAgent(2 * time.Minute); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			return 1
		}
	}

	order := []string{"mysql", "redis", "openresty"}
	for _, key := range order {
		app := findApp(meta, key)
		if app == nil {
			continue
		}
		if opts.onlyApps != nil && !opts.onlyApps[key] {
			continue
		}
		status, message, installed := installedStatus(db, key)
		if installed {
			if status == statusRunning {
				fmt.Printf("==> %s %s 已安装并运行，跳过。\n", app.Name, app.Version)
				continue
			}
			fmt.Printf("警告: %s 已存在安装记录（状态 %s: %s），跳过自动安装。\n", app.Name, status, message)
			continue
		}
		detailID, version, err := appDetailID(db, key, app.Version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			return 1
		}
		params := buildInstallParams(app, &opts)
		payload := map[string]interface{}{
			"appDetailId":  detailID,
			"params":       params,
			"name":         key,
			"advanced":      true,
			"cpuQuota":      0,
			"memoryLimit":   0,
			"memoryUnit":    "M",
			"allowPort":     opts.allowExternal,
			"editCompose":   false,
			"pullImage":     false,
			"gpuConfig":     false,
			"taskID":        newUUID(),
			"restartPolicy": "always",
		}
		if opts.dryRun {
			data, _ := json.MarshalIndent(payload, "", "  ")
			fmt.Printf("==> dry-run %s %s (detail %d)\n%s\n", app.Name, version, detailID, data)
			continue
		}
		fmt.Printf("==> 安装 %s %s（端口参数见下方）...\n", app.Name, version)
		installID, err := client.install(payload)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: 提交 %s 安装失败: %v\n", app.Name, err)
			return 1
		}
		if err := client.waitInstall(installID, app.Name, opts.timeout); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %s 安装失败: %v\n", app.Name, err)
			return 1
		}
		if key == "mysql" {
			updateDatabasePort(opts.baseDir, key, opts.mysqlPort)
		}
		if key == "redis" {
			updateDatabasePort(opts.baseDir, key, opts.redisPort)
		}
		fmt.Printf("==> %s 安装完成。\n", app.Name)
	}

	fmt.Println()
	fmt.Println("================ 应用安装结果 ================")
	ok, err := printInstalledStatus(db, meta)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Println("==============================================")
	if findApp(meta, "mysql") != nil {
		fmt.Printf("MySQL root 密码: %s\n", opts.mysqlPassword)
		fmt.Printf("MySQL 端口:      %d\n", opts.mysqlPort)
	}
	if findApp(meta, "redis") != nil {
		fmt.Printf("Redis 密码:      %s\n", opts.redisPassword)
		fmt.Printf("Redis 端口:      %d\n", opts.redisPort)
	}
	if findApp(meta, "openresty") != nil {
		fmt.Printf("OpenResty HTTP/HTTPS 端口: %d/%d\n", opts.openrestyHTTPPort, opts.openrestyHTTPS)
	}
	if opts.dryRun {
		return 0
	}
	if !ok {
		return 1
	}
	return 0
}

func buildInstallParams(app *AppMeta, opts *installOptions) map[string]interface{} {
	overrides := map[string]interface{}{}
	switch app.Key {
	case "mysql":
		overrides["PANEL_DB_ROOT_PASSWORD"] = opts.mysqlPassword
		overrides["PANEL_APP_PORT_HTTP"] = opts.mysqlPort
	case "redis":
		overrides["PANEL_REDIS_ROOT_PASSWORD"] = opts.redisPassword
		overrides["PANEL_APP_PORT_HTTP"] = opts.redisPort
	case "openresty":
		overrides["PANEL_APP_PORT_HTTP"] = opts.openrestyHTTPPort
		overrides["PANEL_APP_PORT_HTTPS"] = opts.openrestyHTTPS
		overrides["WEBSITE_DIR"] = "www"
	}

	params := map[string]interface{}{}
	var form AppForm
	if len(app.Params) > 0 {
		_ = json.Unmarshal(app.Params, &form)
	}
	for _, field := range form.FormFields {
		if field.EnvKey == "" {
			continue
		}
		if value, ok := overrides[field.EnvKey]; ok {
			params[field.EnvKey] = value
			continue
		}
		if field.Default != nil {
			params[field.EnvKey] = field.Default
		}
	}
	for key, value := range overrides {
		params[key] = value
	}
	return params
}

func openAgentDBReadOnly(baseDir string) (*sql.DB, error) {
	dbPath := filepath.Join(baseDir, "1panel", "db", "agent.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("1Panel database not found at %s: %w", dbPath, err)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, fmt.Errorf("open 1Panel database: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// updateDatabasePort fixes the port shown on the 1Panel "Databases" page when
// the user chose a non-default MySQL/Redis port. 1Panel derives the database
// record port from the form default, not from the actual install parameters.
func updateDatabasePort(baseDir, key string, port int) {
	dbPath := filepath.Join(baseDir, "1panel", "db", "agent.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, _ = db.Exec(`UPDATE databases SET port=? WHERE type=? AND app_install_id=(SELECT id FROM app_installs WHERE name=? ORDER BY id DESC LIMIT 1)`, port, key, key)
}

func installedStatus(db *sql.DB, name string) (string, string, bool) {
	var status, message string
	err := db.QueryRow(`SELECT status, COALESCE(message,'') FROM app_installs WHERE name=? ORDER BY id DESC LIMIT 1`, name).Scan(&status, &message)
	if err == sql.ErrNoRows {
		return "", "", false
	}
	if err != nil {
		return "", "", false
	}
	return status, message, true
}

func appDetailID(db *sql.DB, key, version string) (uint, string, error) {
	var id uint
	var foundVersion string
	err := db.QueryRow(`SELECT d.id, d.version FROM app_details d JOIN apps a ON a.id=d.app_id WHERE a.key=? AND a.resource='remote' AND d.version=? ORDER BY d.id DESC LIMIT 1`, key, version).Scan(&id, &foundVersion)
	if err != nil {
		return 0, "", fmt.Errorf("app %s %s is not registered in the panel database: %w", key, version, err)
	}
	return id, foundVersion, nil
}

func (c *panelClient) install(payload map[string]interface{}) (uint, error) {
	data, code, err := c.request(http.MethodPost, "apps/install", payload, nil)
	if err != nil {
		return 0, err
	}
	res, err := parseAPIResponse(data)
	if err != nil {
		return 0, err
	}
	if code != http.StatusOK || res.Code != 200 {
		return 0, fmt.Errorf("HTTP %d, code %d: %s", code, res.Code, truncate(res.Message, 500))
	}
	var result struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(res.Data, &result); err != nil {
		return 0, fmt.Errorf("parse install response: %w", err)
	}
	if result.ID == 0 {
		return 0, fmt.Errorf("panel returned an empty install id")
	}
	return result.ID, nil
}

func (c *panelClient) waitInstall(id uint, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	path := fmt.Sprintf("apps/installed/info/%d", id)
	lastStatus := ""
	lastLog := time.Now()
	for time.Now().Before(deadline) {
		data, code, err := c.request(http.MethodGet, path, nil, nil)
		if err == nil && code == http.StatusOK {
			res, err := parseAPIResponse(data)
			if err == nil && res.Code == 200 {
				var info struct {
					Status  string `json:"status"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal(res.Data, &info); err == nil {
					if info.Status != lastStatus {
						fmt.Printf("    %s 状态: %s\n", name, info.Status)
						lastStatus = info.Status
					}
					switch info.Status {
					case statusRunning:
						return nil
					case "InstallErr", "UpErr", "DownloadErr", "Deleted":
						return fmt.Errorf("状态 %s: %s", info.Status, strings.TrimSpace(info.Message))
					}
				}
			}
		} else if err != nil {
			if time.Since(lastLog) > 30*time.Second {
				fmt.Printf("    等待 %s: %v\n", name, err)
				lastLog = time.Now()
			}
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("等待超时（%s），最后状态: %s", timeout, lastStatus)
}

func printInstalledStatus(db *sql.DB, meta *BundleMeta) (bool, error) {
	allOK := true
	for _, app := range meta.Apps {
		status, message, installed := installedStatus(db, app.Key)
		if !installed {
			fmt.Printf("  %-10s %-24s 未安装\n", app.Name, app.Version)
			allOK = false
			continue
		}
		mark := "OK"
		if status != statusRunning {
			mark = "FAIL"
			allOK = false
		}
		fmt.Printf("  %-10s %-24s %-6s %s\n", app.Name, app.Version, mark, status)
		if status != statusRunning && message != "" {
			fmt.Printf("      %s\n", truncate(message, 300))
		}
	}
	for _, key := range []string{"mysql", "redis"} {
		var name, dbType, address string
		var port int
		err := db.QueryRow(`SELECT name,type,address,port FROM databases WHERE type=? AND app_install_id>0 ORDER BY id DESC LIMIT 1`, key).Scan(&name, &dbType, &address, &port)
		if err == nil {
			fmt.Printf("  数据库记录: %-8s %-8s %s:%d\n", name, dbType, address, port)
		}
	}
	return allOK, nil
}

func randomPassword(n int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	if n <= 0 {
		n = 24
	}
	out := make([]byte, n)
	raw := make([]byte, n*2)
	if _, err := rand.Read(raw); err != nil {
		return "ChangeMe1234567890abcdef"
	}
	for i := 0; i < n; i++ {
		out[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(out)
}

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("offline-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
