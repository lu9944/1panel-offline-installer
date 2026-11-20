package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func runVerify(args []string) int {
	fsys := flag.NewFlagSet("verify", flag.ExitOnError)
	baseDir := fsys.String("base-dir", "/opt", "1Panel base directory")
	metaPath := fsys.String("meta", "", "path to meta/apps.json (optional)")
	expect := fsys.String("expect", "", "comma-separated expected apps, e.g. mysql=8.0.46,redis=8.10.3,openresty")
	if err := fsys.Parse(args); err != nil {
		return 2
	}

	var meta *BundleMeta
	var err error
	if *metaPath != "" {
		meta, err = loadBundleMeta(*metaPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			return 1
		}
	}

	type expectedApp struct {
		key     string
		version string
	}
	var expected []expectedApp
	if *expect != "" {
		for _, item := range strings.Split(*expect, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			parts := strings.SplitN(item, "=", 2)
			entry := expectedApp{key: parts[0]}
			if len(parts) == 2 {
				entry.version = parts[1]
			}
			expected = append(expected, entry)
		}
	} else if meta != nil {
		for _, app := range meta.Apps {
			expected = append(expected, expectedApp{key: app.Key, version: app.Version})
		}
	}

	db, err := openAgentDBReadOnly(*baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}
	defer db.Close()

	if meta != nil {
		fmt.Println("已注册应用商店应用:")
		for _, app := range meta.Apps {
			fmt.Printf("  %-12s %s\n", app.Key, app.Version)
		}
	}

	allOK := true
	for _, item := range expected {
		status, message, installed := installedStatus(db, item.key)
		if !installed {
			fmt.Printf("FAIL  %-12s 未安装\n", item.key)
			allOK = false
			continue
		}
		var version string
		_ = db.QueryRow(`SELECT version FROM app_installs WHERE name=? ORDER BY id DESC LIMIT 1`, item.key).Scan(&version)
		if item.version != "" && version != item.version {
			fmt.Printf("FAIL  %-12s 版本不匹配: 期望 %s，实际 %s\n", item.key, item.version, version)
			allOK = false
			continue
		}
		if status != statusRunning {
			fmt.Printf("FAIL  %-12s %s: %s\n", item.key, status, truncate(message, 300))
			allOK = false
			continue
		}
		fmt.Printf("OK    %-12s %s %s\n", item.key, version, status)
	}

	for _, key := range []string{"mysql", "redis"} {
		var count int
		_ = db.QueryRow(`SELECT COUNT(*) FROM databases WHERE type=? AND app_install_id>0`, key).Scan(&count)
		if count > 0 {
			fmt.Printf("OK    数据库记录 %-6s 已关联到 1Panel 数据库管理\n", key)
		} else {
			fmt.Printf("WARN  数据库记录 %-6s 未找到\n", key)
		}
	}

	if !allOK {
		return 1
	}
	fmt.Println("验证通过。")
	return 0
}
