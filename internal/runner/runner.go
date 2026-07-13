package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

type Runner interface {
	Run(context.Context, string, ...string) error
	LookPath(string) error
}
type Exec struct {
	Dir      string
	Out, Err io.Writer
}

func (e Exec) Run(ctx context.Context, n string, a ...string) error {
	c := exec.CommandContext(ctx, n, a...)
	c.Dir = e.Dir
	c.Stdout = e.Out
	c.Stderr = e.Err
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %w", n, err)
	}
	return nil
}
func (e Exec) LookPath(n string) error { _, err := exec.LookPath(n); return err }
