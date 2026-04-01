package agent

import (
	"strings"
	"testing"

	pb "github.com/nchapman/hiro/internal/ipc/proto"
)

func TestFormatJobNotification_Completed(t *testing.T) {
	n := formatJobNotification(&pb.JobCompletion{
		TaskId:      "00000A",
		Command:     "npm run build",
		Description: "Build the project",
		ExitCode:    0,
		Failed:      false,
	})

	if n.Source != "background-job" {
		t.Errorf("source = %q, want background-job", n.Source)
	}
	if !strings.Contains(n.Content, "<task-notification>") {
		t.Error("expected <task-notification> tag")
	}
	if !strings.Contains(n.Content, "<task_id>00000A</task_id>") {
		t.Error("expected task_id")
	}
	if !strings.Contains(n.Content, "<status>completed</status>") {
		t.Error("expected status=completed")
	}
	if !strings.Contains(n.Content, "Build the project") {
		t.Error("expected description in summary")
	}
	if !strings.Contains(n.Content, "exit code 0") {
		t.Error("expected exit code in summary")
	}
}

func TestFormatJobNotification_Failed(t *testing.T) {
	n := formatJobNotification(&pb.JobCompletion{
		TaskId:   "00000B",
		Command:  "make test",
		ExitCode: 1,
		Failed:   true,
	})

	if !strings.Contains(n.Content, "<status>failed</status>") {
		t.Error("expected status=failed")
	}
	if !strings.Contains(n.Content, "failed with exit code 1") {
		t.Error("expected failure summary")
	}
	// When description is empty, command should be used.
	if !strings.Contains(n.Content, "make test") {
		t.Error("expected command used as description fallback")
	}
}
