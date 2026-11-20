package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const sqliteTimeLayout = "2006-01-02 15:04:05.999999999-07:00"

func runSeed(args []string) int {
	fsys := flag.NewFlagSet("seed", flag.ExitOnError)
	baseDir := fsys.String("base-dir", "/opt", "1Panel base directory (the parent of the 1panel directory)")
	appsDir := fsys.String("apps-dir", "", "directory containing app definitions (required)")
	metaPath := fsys.String("meta", "", "path to meta/apps.json (required)")
	storeVersion := fsys.Int64("store-version", 0, "app-store revision stored in the panel DB (default: from metadata)")
	noCopy := fsys.Bool("no-copy", false, "only update the database, do not copy app files")
	if err := fsys.Parse(args); err != nil {
		return 2
	}
	if *appsDir == "" || *metaPath == "" {
		fmt.Fprintln(os.Stderr, "seed: --apps-dir and --meta are required")
		return 2
	}

	meta, err := loadBundleMeta(*metaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}
	version := *storeVersion
	if version <= 0 {
		version = meta.AppStoreVersion
	}

	resourceRoot := filepath.Join(*baseDir, "1panel", "resource", "apps", "remote")
	dbPath := filepath.Join(*baseDir, "1panel", "db", "agent.db")
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: 1Panel database not found at %s (is 1Panel installed?)\n", dbPath)
		return 1
	}
	if err := os.MkdirAll(resourceRoot, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: create app resource directory: %v\n", err)
		return 1
	}

	for i := range meta.Apps {
		app := &meta.Apps[i]
		if !*noCopy {
			src := filepath.Join(*appsDir, app.Key)
			dst := filepath.Join(resourceRoot, app.Key)
			if err := copyTree(src, dst); err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: copy %s: %v\n", app.Key, err)
				return 1
			}
		}
		stampDir := filepath.Join(resourceRoot, app.Key, app.Version)
		if err := os.MkdirAll(stampDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: create version directory %s: %v\n", stampDir, err)
			return 1
		}
		stamp := filepath.Join(stampDir, ".1panel-appstore-version")
		if err := os.WriteFile(stamp, []byte(strconv.FormatInt(version, 10)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: write cache stamp %s: %v\n", stamp, err)
			return 1
		}
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: open 1Panel database: %v\n", err)
		return 1
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := seedDatabase(db, meta, version); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: seed 1Panel database: %v\n", err)
		return 1
	}

	fmt.Printf("已注册应用: ")
	parts := make([]string, 0, len(meta.Apps))
	for i := range meta.Apps {
		parts = append(parts, fmt.Sprintf("%s %s", meta.Apps[i].Name, meta.Apps[i].Version))
	}
	fmt.Println(strings.Join(parts, ", "))
	fmt.Printf("应用商店版本号: %d\n", version)
	return 0
}

func seedDatabase(db *sql.DB, meta *BundleMeta, storeVersion int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Format(sqliteTimeLayout)
	for i := range meta.Apps {
		app := &meta.Apps[i]
		if err := removeUnusedLocalApp(tx, app.Key); err != nil {
			return err
		}
		appID, err := upsertApp(tx, app, storeVersion, now)
		if err != nil {
			return fmt.Errorf("upsert app %s: %w", app.Key, err)
		}
		if err := upsertAppDetail(tx, appID, app, storeVersion, now); err != nil {
			return fmt.Errorf("upsert app detail %s: %w", app.Key, err)
		}
		if err := replaceAppTags(tx, appID, app.Tags, now); err != nil {
			return fmt.Errorf("upsert app tags %s: %w", app.Key, err)
		}
	}

	if err := setSetting(tx, "AppStoreSyncStatus", "SyncSuccess", now); err != nil {
		return err
	}
	if err := setSetting(tx, "AppStoreLastModified", strconv.FormatInt(storeVersion, 10), now); err != nil {
		return err
	}
	if err := setSetting(tx, "AppStoreVersion", meta.BundleVersion, now); err != nil {
		return err
	}
	return tx.Commit()
}

// removeUnusedLocalApp removes local-app duplicates left behind by older
// offline import attempts. Apps that are referenced by an installation are
// never removed.
func removeUnusedLocalApp(tx *sql.Tx, key string) error {
	rows, err := tx.Query(`SELECT id FROM apps WHERE key=? AND resource='local' AND id NOT IN (SELECT app_id FROM app_installs)`, key)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM app_details WHERE app_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM app_tags WHERE app_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM apps WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func upsertApp(tx *sql.Tx, app *AppMeta, storeVersion int64, now string) (int64, error) {
	desc, _ := json.Marshal(app.Description)
	arch := strings.Join(app.Architectures, ",")
	values := []interface{}{
		now, now, app.Name, app.Key, app.ShortDescZh, app.ShortDescEn, string(desc), app.IconBase64,
		app.Type, "Normal", "", boolInt(app.CrossVersionUpdate), app.Limit, app.Website, app.Github,
		app.Document, app.Recommend, "remote", app.ReadMe, storeVersion, arch, 0,
		boolInt(false), 0.0, boolInt(app.BatchInstallSupport),
	}

	var id int64
	err := tx.QueryRow(`SELECT id FROM apps WHERE key=? AND resource='remote' ORDER BY id LIMIT 1`, app.Key).Scan(&id)
	if err == sql.ErrNoRows {
		res, err := tx.Exec("INSERT INTO apps ("+
			"`created_at`,`updated_at`,`name`,`key`,`short_desc_zh`,`short_desc_en`,`description`,`icon`,`type`,`status`,`required`,`cross_version_update`,`limit`,`website`,`github`,`document`,`recommend`,`resource`,`read_me`,`last_modified`,`architectures`,`memory_required`,`gpu_support`,`required_panel_version`,`batch_install_support`) "+
			"VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", values...)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec("UPDATE apps SET "+
		"`updated_at`=?,`name`=?,`short_desc_zh`=?,`short_desc_en`=?,`description`=?,`icon`=?,`type`=?,`status`=?,`required`=?,`cross_version_update`=?,`limit`=?,`website`=?,`github`=?,`document`=?,`recommend`=?,`resource`=?,`read_me`=?,`last_modified`=?,`architectures`=?,`memory_required`=?,`gpu_support`=?,`required_panel_version`=?,`batch_install_support`=? WHERE id=?",
		now, app.Name, app.ShortDescZh, app.ShortDescEn, string(desc), app.IconBase64, app.Type, "Normal", "",
		boolInt(app.CrossVersionUpdate), app.Limit, app.Website, app.Github, app.Document, app.Recommend,
		"remote", app.ReadMe, storeVersion, arch, 0, boolInt(false), 0.0, boolInt(app.BatchInstallSupport), id)
	return id, err
}

func upsertAppDetail(tx *sql.Tx, appID int64, app *AppMeta, storeVersion int64, now string) error {
	params := string(app.Params)
	if strings.TrimSpace(params) == "" {
		params = "{}"
	}
	var id int64
	err := tx.QueryRow(`SELECT id FROM app_details WHERE app_id=? AND version=? ORDER BY id LIMIT 1`, appID, app.Version).Scan(&id)
	if err == sql.ErrNoRows {
		_, err = tx.Exec("INSERT INTO app_details ("+
			"`created_at`,`updated_at`,`app_id`,`version`,`params`,`docker_compose`,`status`,`last_version`,`last_modified`,`download_url`,`download_call_back_url`,`update`) "+
			"VALUES (?,?,?,?,?,?,'Normal','',?,'','',0)",
			now, now, appID, app.Version, params, app.DockerCompose, storeVersion)
		return err
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE app_details SET `updated_at`=?,`params`=?,`docker_compose`=?,`status`='Normal',`last_modified`=?,`download_url`='',`download_call_back_url`='',`update`=0 WHERE id=?",
		now, params, app.DockerCompose, storeVersion, id)
	return err
}

func replaceAppTags(tx *sql.Tx, appID int64, tags []string, now string) error {
	if _, err := tx.Exec(`DELETE FROM app_tags WHERE app_id=?`, appID); err != nil {
		return err
	}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		var tagID int64
		err := tx.QueryRow(`SELECT id FROM tags WHERE key=? ORDER BY id LIMIT 1`, tag).Scan(&tagID)
		if err == sql.ErrNoRows {
			translations, _ := json.Marshal(map[string]string{"en": tag, "zh": tag})
			res, err := tx.Exec("INSERT INTO tags (`created_at`,`updated_at`,`key`,`name`,`sort`,`translations`) VALUES (?,?,?,?,1,?)",
				now, now, tag, tag, string(translations))
			if err != nil {
				return err
			}
			tagID, err = res.LastInsertId()
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO app_tags (`created_at`,`updated_at`,`app_id`,`tag_id`) VALUES (?,?,?,?)", now, now, appID, tagID); err != nil {
			return err
		}
	}
	return nil
}

func setSetting(tx *sql.Tx, key, value, now string) error {
	res, err := tx.Exec("UPDATE settings SET `value`=?,`updated_at`=? WHERE `key`=?", value, now, key)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		_, err = tx.Exec("INSERT INTO settings (`created_at`,`updated_at`,`key`,`value`,`about`) VALUES (?,?,?,?,'')", now, now, key, value)
	}
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, fi.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
