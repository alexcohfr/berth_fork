package box

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"

	"github.com/cosscom/shipyard/internal/agentpath"
)

// Rebases and merges make commits, which git refuses without an identity;
// a fresh CI machine has none.
func TestMain(m *testing.M) {
	// Exercise the production session launcher command with this test binary.
	if os.Getenv("BERTH_OPENCODE_TEST_HELPER") == "1" && len(os.Args) > 2 && os.Args[1] == "opencode" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		if err := RunOpenCode(ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "berth test", "GIT_AUTHOR_EMAIL": "test@example.com",
		"GIT_COMMITTER_NAME": "berth test", "GIT_COMMITTER_EMAIL": "test@example.com",
	} {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
	// Agent CLIs are looked for on the test's PATH and HOME each time, never
	// through the developer's own shell.
	for _, k := range []string{"NVM_DIR", "FNM_DIR", "VOLTA_HOME", "BUN_INSTALL", "PNPM_HOME", "XDG_DATA_HOME"} {
		os.Unsetenv(k)
	}
	testFinder := &agentpath.Finder{NoCache: true, NoVersion: true, NoNPM: true, SystemDirs: []string{}}
	agentFinder = func() *agentpath.Finder { return testFinder }
	os.Exit(m.Run())
}
