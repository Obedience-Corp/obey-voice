// internal/buildutil/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	buildutil "github.com/Obedience-Corp/build-util"
)

func main() {
	args := os.Args[1:]
	if isIntegrationCommand(args) {
		addBuildTag("integration")
	}

	if len(args) > 0 && args[0] == "install" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "usage: buildutil install")
			os.Exit(1)
		}
		if err := installCheckout(ctx, projectConfig()); err != nil {
			fmt.Fprintln(os.Stderr, "install:", err)
			os.Exit(1)
		}
		return
	}
	buildutil.Run(args, projectConfig())
}

func projectConfig() buildutil.BuildConfig {
	return buildutil.BuildConfig{
		BinaryName:         "obey-voice",
		MainPath:           "./cmd/obey-voice",
		SectionName:        "Obey Voice",
		TestTimeout:        2 * time.Minute,
		IntegrationTestDir: "tests/integration",
		IntegrationBuildEnv: func() []string {
			// CGO_ENABLED=0 for CLI/config integration tests.
			// Audio-dependent tests run locally, not in containers.
			return []string{
				"CGO_ENABLED=0",
				"GOOS=linux",
				"GOARCH=" + runtime.GOARCH,
			}
		},
	}
}

func isIntegrationCommand(args []string) bool {
	for _, arg := range args {
		if arg == "integration" {
			return true
		}
	}
	return false
}

func addBuildTag(tag string) {
	current := strings.TrimSpace(os.Getenv("BUILD_TAGS"))
	if current == "" {
		_ = os.Setenv("BUILD_TAGS", tag)
		return
	}
	for _, existing := range strings.Split(current, ",") {
		if strings.TrimSpace(existing) == tag {
			return
		}
	}
	_ = os.Setenv("BUILD_TAGS", current+","+tag)
}
