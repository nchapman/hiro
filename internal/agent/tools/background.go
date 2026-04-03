package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// cappedBuffer is a thread-safe writer that drops data beyond a size limit.
type cappedBuffer struct {
	mu   sync.RWMutex
	buf  []byte
	lost int64
}

func (cb *cappedBuffer) Write(p []byte) (n int, err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	avail := maxBufferBytes - len(cb.buf)
	if avail <= 0 {
		cb.lost += int64(len(p))
		return len(p), nil
	}
	if len(p) > avail {
		cb.lost += int64(len(p) - avail)
		p = p[:avail]
	}
	cb.buf = append(cb.buf, p...)
	return len(p), nil
}

func (cb *cappedBuffer) String() string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return string(cb.buf)
}

// BackgroundJob represents a shell command running in the background.
type BackgroundJob struct {
	ID          string
	Command     string
	Description string
	WorkingDir  string
	cancel      context.CancelFunc
	stdout      *cappedBuffer
	stderr      *cappedBuffer
	done        chan struct{}
	exitErr     error
	completedAt int64       // unix timestamp, 0 if still running
	notified    atomic.Bool // true once completion notification has been sent/suppressed
}

// ExitErr returns the exit error from the completed job, or nil if still running or succeeded.
func (j *BackgroundJob) ExitErr() error {
	return j.exitErr
}

// GetOutput returns the current stdout, stderr, completion status, and exit error.
func (j *BackgroundJob) GetOutput() (stdout, stderr string, done bool, err error) {
	select {
	case <-j.done:
		return j.stdout.String(), j.stderr.String(), true, j.exitErr
	default:
		return j.stdout.String(), j.stderr.String(), false, nil
	}
}

// Wait blocks until the job completes or the context is cancelled.
func (j *BackgroundJob) Wait(ctx context.Context) bool {
	select {
	case <-j.done:
		return true
	case <-ctx.Done():
		return false
	}
}

// BackgroundJobManager tracks running background jobs.
type BackgroundJobManager struct {
	mu         sync.RWMutex
	jobs       map[string]*BackgroundJob
	envFn      func() []string          // extra env vars injected into every command (e.g. secrets)
	OnComplete func(job *BackgroundJob) // called when a backgrounded job completes
}

var jobIDCounter atomic.Uint64

// NewBackgroundJobManager creates a new job manager. envFn, if non-nil,
// is called at each Start() to get extra environment variables (e.g.
// secrets from the control plane). It is called lazily so updates to
// secrets take effect on the next command.
func NewBackgroundJobManager(envFn func() []string) *BackgroundJobManager {
	return &BackgroundJobManager{
		jobs:  make(map[string]*BackgroundJob),
		envFn: envFn,
	}
}

// Start creates and starts a new background job. The job runs immediately
// but does NOT trigger OnComplete by default. Call NotifyOnComplete to
// opt in to completion notifications for jobs that are truly backgrounded.
func (m *BackgroundJobManager) Start(workingDir, command string) (*BackgroundJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cleanupLocked()

	if len(m.jobs) >= MaxBackgroundJobs {
		return nil, fmt.Errorf("maximum background jobs (%d) reached — terminate or wait for some to complete", MaxBackgroundJobs)
	}

	id := fmt.Sprintf("%06X", jobIDCounter.Add(1))

	ctx, cancel := context.WithCancel(context.Background())

	job := &BackgroundJob{
		ID:         id,
		Command:    command,
		WorkingDir: workingDir,
		cancel:     cancel,
		stdout:     &cappedBuffer{},
		stderr:     &cappedBuffer{},
		done:       make(chan struct{}),
	}

	m.jobs[id] = job

	go func() {
		defer close(job.done)
		cmd := exec.CommandContext(ctx, "bash", "-c", command)
		cmd.Dir = workingDir
		cmd.Stdout = job.stdout
		cmd.Stderr = job.stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if m.envFn != nil {
			if extra := m.envFn(); len(extra) > 0 {
				cmd.Env = append(os.Environ(), extra...)
			}
		}
		cmd.Cancel = func() error {
			// Kill the entire process group, not just the lead process.
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		cmd.WaitDelay = waitDelay

		if err := cmd.Start(); err != nil {
			job.exitErr = err
			atomic.StoreInt64(&job.completedAt, time.Now().Unix())
			return
		}

		job.exitErr = cmd.Wait()
		atomic.StoreInt64(&job.completedAt, time.Now().Unix())
	}()

	return job, nil
}

// NotifyOnComplete starts a goroutine that waits for the job to complete
// and fires OnComplete. Called by the bash tool when a job is truly
// backgrounded (explicit or auto-background). Jobs consumed synchronously
// never call this, so they never generate notifications. Kill suppresses
// the notification via the notified flag.
func (m *BackgroundJobManager) NotifyOnComplete(id string) {
	if m.OnComplete == nil {
		return
	}
	m.mu.RLock()
	job, ok := m.jobs[id]
	m.mu.RUnlock()
	if !ok {
		return
	}
	go func() {
		<-job.done
		if job.notified.CompareAndSwap(false, true) {
			m.OnComplete(job)
		}
	}()
}

// Get retrieves a job by ID.
func (m *BackgroundJobManager) Get(id string) (*BackgroundJob, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	return j, ok
}

// Kill terminates a job and removes it. Returns an error if the job is not
// found or does not exit within a reasonable timeout.
// Suppresses the OnComplete callback.
func (m *BackgroundJobManager) Kill(id string) error {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("background job not found: %s", id)
	}
	delete(m.jobs, id)
	m.mu.Unlock()

	j.notified.Store(true) // suppress callback BEFORE cancel
	j.cancel()
	select {
	case <-j.done:
		return nil
	case <-time.After(killTimeout):
		return fmt.Errorf("background job %s did not exit within %s", id, killTimeout)
	}
}

// KillAll terminates all running jobs. Used for agent shutdown.
func (m *BackgroundJobManager) KillAll() {
	m.mu.Lock()
	jobs := make([]*BackgroundJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.jobs = make(map[string]*BackgroundJob)
	m.mu.Unlock()

	for _, j := range jobs {
		j.notified.Store(true)
		j.cancel()
	}
	// Wait for all to finish with a bounded timeout.
	deadline := time.After(killTimeout)
	for _, j := range jobs {
		select {
		case <-j.done:
		case <-deadline:
			return
		}
	}
}

// Remove removes a completed job without killing it.
func (m *BackgroundJobManager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, id)
}

// cleanupLocked removes completed jobs past the retention window. Must hold mu.
func (m *BackgroundJobManager) cleanupLocked() {
	cutoff := time.Now().Add(-completedJobRetention).Unix()
	for id, j := range m.jobs {
		t := atomic.LoadInt64(&j.completedAt)
		if t > 0 && t < cutoff {
			delete(m.jobs, id)
		}
	}
}
