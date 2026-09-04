package delegation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("VOICE_DELEGATION_CLI_FIXTURE"); mode != "" {
		if path := os.Getenv("VOICE_DELEGATION_ARGV_FILE"); path != "" {
			b, _ := json.Marshal(os.Args[1:])
			_ = os.WriteFile(path, b, 0600)
		}
		switch mode {
		case "valid":
			if len(os.Args) > 2 && os.Args[2] == "capabilities" {
				_, _ = os.Stdout.WriteString(`{"schema_version":1,"managed_child":true,"start":true,"await":true,"cancel":true}`)
			} else {
				_, _ = os.Stdout.WriteString(`{"delegation_id":"obey-42","state":"working"}`)
			}
		case "old":
			_, _ = os.Stdout.WriteString(`{"schema_version":1,"managed_child":true,"start":true}`)
		case "trailing":
			_, _ = os.Stdout.WriteString(`{} {}`)
		case "large":
			_, _ = os.Stdout.WriteString(strings.Repeat("x", maxCLIOutput+1))
		case "failure":
			_, _ = os.Stderr.WriteString("credential-not-for-model")
			os.Exit(1)
		case "slow":
			time.Sleep(time.Minute)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func fixtureCLI(t *testing.T, mode string) *CLI {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOICE_DELEGATION_CLI_FIXTURE", mode)
	c, err := NewCLI(CLIConfig{Binary: exe, CampaignID: "campaign", ParentSessionID: "parent", Profile: "read-only", Socket: "/tmp/obey socket"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestCLILiteralArgumentsAndCapabilityProbe(t *testing.T) {
	c := fixtureCLI(t, "valid")
	argsPath := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("VOICE_DELEGATION_ARGV_FILE", argsPath)
	if err := c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	task := "inspect $(touch /tmp/should-not-exist); `echo secret`\nwith newlines"
	got, err := c.Start(context.Background(), Request{Task: task, Key: "ticket"})
	if err != nil || got.ID != "obey-42" {
		t.Fatalf("snapshot=%+v err=%v", got, err)
	}
	b, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(b, &args); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for i := 2; i < len(args)-1; i++ {
		values[args[i]] = args[i+1]
	}
	if values["--task"] != task || values["--socket"] != "/tmp/obey socket" || values["--parent-session-id"] != "parent" || values["--profile"] != "read-only" {
		t.Fatalf("args=%q", args)
	}
}
func TestCLIRejectsIncompleteOrInvalidBackend(t *testing.T) {
	for _, mode := range []string{"old", "trailing", "large", "failure"} {
		t.Run(mode, func(t *testing.T) {
			c := fixtureCLI(t, mode)
			err := c.Probe(context.Background())
			if err == nil {
				t.Fatal("invalid backend accepted")
			}
			if strings.Contains(err.Error(), "credential-not-for-model") {
				t.Fatal("stderr leaked")
			}
		})
	}
}
func TestCLIAwaitCancellationEndsOnlyWaitProcess(t *testing.T) {
	c := fixtureCLI(t, "slow")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Await(ctx, "obey-42"); err == nil {
		t.Fatal("wait did not stop")
	}
}
