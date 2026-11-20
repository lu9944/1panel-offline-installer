package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// runPatch flips the embedded "is_offline: false" setting in already-installed
// 1Panel binaries. It is used with --skip-panel-install so an existing panel
// also keeps the bundled app store after a restart.
func runPatch(args []string) int {
	fsys := flag.NewFlagSet("patch", flag.ExitOnError)
	binaryDir := fsys.String("binary-dir", "/usr/local/bin", "directory containing 1panel-agent and 1panel-core")
	if err := fsys.Parse(args); err != nil {
		return 2
	}
	old := []byte("is_offline: false")
	current := []byte("is_offline: true ")
	changed := 0
	for _, name := range []string{"1panel-agent", "1panel-core"} {
		path := filepath.Join(*binaryDir, name)
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: 打开 %s 失败: %v\n", path, err)
			return 1
		}
		data, err := io.ReadAll(file)
		if err != nil {
			file.Close()
			fmt.Fprintf(os.Stderr, "ERROR: 读取 %s 失败: %v\n", path, err)
			return 1
		}
		if bytes.Contains(data, current) {
			file.Close()
			fmt.Printf("%s: 已处于离线模式\n", name)
			continue
		}
		offsets := make([]int, 0)
		for start := 0; ; {
			index := bytes.Index(data[start:], old)
			if index < 0 {
				break
			}
			offsets = append(offsets, start+index)
			start += index + len(old)
		}
		if len(offsets) == 0 {
			file.Close()
			fmt.Fprintf(os.Stderr, "ERROR: %s 中未找到 is_offline 配置，无法启用离线模式\n", path)
			return 1
		}
		for _, offset := range offsets {
			if _, err := file.WriteAt(current, int64(offset)); err != nil {
				file.Close()
				fmt.Fprintf(os.Stderr, "ERROR: 写入 %s 失败: %v\n", path, err)
				return 1
			}
		}
		if err := file.Sync(); err != nil {
			file.Close()
			fmt.Fprintf(os.Stderr, "ERROR: 同步 %s 失败: %v\n", path, err)
			return 1
		}
		file.Close()
		changed++
		fmt.Printf("%s: 已启用离线模式\n", name)
	}
	if changed == 0 {
		fmt.Println("1Panel 二进制已处于离线模式")
	}
	return 0
}
