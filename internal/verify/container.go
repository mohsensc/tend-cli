package verify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

var lookPath = exec.LookPath

// workDir is where the worktree sits inside the container, and so the prefix
// of every path the commands print there.
const workDir = "/work"

// hostOnly names describe the machine tend runs on and mean nothing, or
// something wrong, inside the container.
var hostOnly = map[string]bool{
	"HOME": true, "PATH": true, "TMPDIR": true, "PWD": true, "OLDPWD": true, "SHELL": true,
	"USER": true, "LOGNAME": true, "TERM": true, "SHLVL": true, "_": true,
}

func containerSteps(dir, image string, lines []string, env []string) ([]step, error) {
	if _, err := lookPath("docker"); err != nil {
		return nil, core.ToolMissing{Tool: "docker"}
	}
	var names []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if name == "" || hostOnly[name] || strings.HasPrefix(name, "DOCKER_") {
			continue
		}
		names = append(names, name)
	}
	res := make([]step, len(lines))
	for i, l := range lines {
		name := containerName()
		res[i] = step{
			cmd:  l,
			argv: dockerArgv(dir, image, l, name, user(), names),
			stop: func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = exec.CommandContext(ctx, "docker", "kill", name).Run()
			},
		}
	}
	return res, nil
}

// dockerArgv is the command line for one step in a container. Values of the
// passed names come from the docker client's own environment, which is the
// filtered one, so nothing is written on the command line.
func dockerArgv(dir, image, line, name, user string, names []string) []string {
	res := []string{"docker", "run", "--rm", "--init", "--name", name, "-v", dir + ":" + workDir, "-w", workDir}
	if user != "" {
		// Files the step writes must stay removable by us, and a uid with no
		// passwd entry has "/" as its home, which npm and pip cannot write to.
		res = append(res, "--user", user, "-e", "HOME=/tmp")
	}
	for _, n := range names {
		res = append(res, "-e", n)
	}
	return append(res, image, "sh", "-c", line)
}

func user() string {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return ""
	}
	return strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
}

func containerName() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "tend-" + hex.EncodeToString(b)
}
