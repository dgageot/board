package loops

import "os/exec"

func isolateProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return cmd.Process.Kill()
	}
}
