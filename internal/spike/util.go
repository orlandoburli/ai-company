package spike

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// countProcesses returns the number of *processes* (not threads) owned by uid.
// `ps -U <uid>` alone emits one line per thread, which massively overcounts.
func countProcesses(ctx context.Context, uid string) (int, error) {
	out, err := exec.CommandContext(ctx, "ps", "-U", uid, "-o", "pid=").Output()
	if err != nil {
		return 0, err
	}
	return len(strings.Fields(string(out))), nil
}

// currentUID returns the numeric uid, used only for the RLIMIT_NPROC datapoint.
func currentUID() string { return strconv.Itoa(os.Getuid()) }
