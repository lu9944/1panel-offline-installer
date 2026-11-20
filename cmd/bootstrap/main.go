// Command 1panel-offline-bootstrap prepares an offline 1Panel installation:
//
//   - seed:    register the bundled application definitions in the 1Panel database
//   - install: install MySQL / Redis / OpenResty through the 1Panel API
//   - verify:  check that the bundled applications are installed and running
//
// The binary is statically linked and uses only Go's standard library plus a
// pure-Go SQLite driver, so it does not depend on Python or any target-side
// packages.
package main

import (
	"fmt"
	"os"
)

var programVersion = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `1panel-offline-bootstrap %s

Usage:
  1panel-offline-bootstrap seed    [options]   Register bundled apps in 1Panel DB
  1panel-offline-bootstrap install [options]   Install bundled apps via 1Panel API
  1panel-offline-bootstrap verify  [options]   Verify installed apps and status
  1panel-offline-bootstrap wait    [options]   Wait for 1Panel agent database migration
  1panel-offline-bootstrap patch   [options]   Enable offline mode in existing 1Panel binaries
  1panel-offline-bootstrap version

Run "1panel-offline-bootstrap <command> -h" for command options.
`, programVersion)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var code int
	switch os.Args[1] {
	case "seed":
		code = runSeed(os.Args[2:])
	case "install":
		code = runInstall(os.Args[2:])
	case "verify":
		code = runVerify(os.Args[2:])
	case "wait":
		code = runWait(os.Args[2:])
	case "patch":
		code = runPatch(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println(programVersion)
		return
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	os.Exit(code)
}
