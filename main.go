package main

import (
	"flag"
	"fmt"
	"os"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		serveCmd := flag.NewFlagSet("serve", flag.ExitOnError)
		port := serveCmd.Int("port", 8080, "HTTP server port")
		maxConcurrent := serveCmd.Int("max-concurrent", 10, "Max concurrent executions")
		timeout := serveCmd.Int("timeout", 30, "Default execution timeout (seconds)")
		maxTimeout := serveCmd.Int("max-timeout", 120, "Maximum allowed timeout (seconds)")
		serveCmd.Parse(os.Args[2:])

		if err := runServe(*port, *maxConcurrent, *timeout, *maxTimeout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "run":
		runCmd := flag.NewFlagSet("run", flag.ExitOnError)
		timeout := runCmd.Int("timeout", 30, "Execution timeout (seconds)")
		runCmd.Parse(os.Args[2:])

		args := runCmd.Args()
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "Error: code argument required\nUsage: sidegent run [--timeout N] 'code'\n")
			os.Exit(1)
		}

		code := args[0]
		os.Exit(runCode(code, *timeout))

	case "--version", "-v", "version":
		fmt.Printf("sidegent v%s\n", version)

	case "--help", "-h", "help":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`sidegent - Run Python code in secure sandboxes

Usage:
  sidegent <command> [flags]

Commands:
  serve   Start the HTTP API server
  run     Execute Python code in a sandbox

Examples:
  sidegent run 'print(42)'
  sidegent run --timeout 10 'import time; time.sleep(5)'
  sidegent serve
  sidegent serve --port 3000

Flags:
  --help      Show help
  --version   Show version
`)
}
