package pve

import (
	"context"
	"fmt"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
)

const taskPollInterval = 2 * time.Second

// WaitTask waits for a PVE task and converts a failed exit status into an
// error. go-proxmox's task.Wait returns nil once the task stops, EVEN IF
// the task failed — IsFailed/ExitStatus must be checked explicitly.
func (c *Client) WaitTask(ctx context.Context, task *proxmox.Task, timeout time.Duration) error {
	if task == nil {
		return nil
	}
	if err := task.Wait(ctx, taskPollInterval, timeout); err != nil {
		if proxmox.IsTimeout(err) {
			return fmt.Errorf("pve task %s did not finish within %s", task.UPID, timeout)
		}
		return fmt.Errorf("waiting for pve task %s: %w", task.UPID, err)
	}
	if task.IsFailed {
		return fmt.Errorf("pve task %s failed: %s", task.UPID, task.ExitStatus)
	}
	return nil
}
