package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// waitForAgent waits until the 1Panel agent has finished its first database
// migration. Stopping the agent too early (immediately after install.sh
// returns) leaves agent.db without the apps table and breaks offline seeding.
func runWait(args []string) int {
	fsys := flag.NewFlagSet("wait", flag.ExitOnError)
	baseDir := fsys.String("base-dir", "/opt", "1Panel base directory")
	timeout := fsys.Duration("timeout", 3*time.Minute, "maximum time to wait")
	if err := fsys.Parse(args); err != nil {
		return 2
	}
	dbPath := filepath.Join(*baseDir, "1panel", "db", "agent.db")
	deadline := time.Now().Add(*timeout)
	var last string
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dbPath); err == nil {
			db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
			if err == nil {
				var count int
				err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='apps'`).Scan(&count)
				_ = db.Close()
				if err == nil && count == 1 {
					fmt.Println("1Panel Agent 数据库已就绪")
					return 0
				}
				if err != nil {
					last = err.Error()
				}
			} else {
				last = err.Error()
			}
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Fprintf(os.Stderr, "ERROR: 等待 1Panel Agent 初始化超时: %s\n", last)
	return 1
}
