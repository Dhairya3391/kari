package main

import (
	"fmt"
	"os"

	"kari/internal/app"
	"kari/internal/logging"
)

func main() {
	if err := logging.Init(true); err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logging: %v\n", err)
		os.Exit(1)
	}
	defer logging.Close()

	if err := app.Run(); err != nil {
		logging.Errorf("app error: %v", err)
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
