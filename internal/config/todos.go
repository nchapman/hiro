package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nchapman/hiro/internal/platform/fsperm"
	"gopkg.in/yaml.v3"
)

const todosFileName = "todos.yaml"

// TodoStatus represents the state of a todo item.
type TodoStatus string

const (
	TodoStatusPending    TodoStatus = "pending"
	TodoStatusInProgress TodoStatus = "in_progress"
	TodoStatusCompleted  TodoStatus = "completed"
)

// Todo represents a single task in an agent's todo list.
type Todo struct {
	Content    string     `yaml:"content"`
	Status     TodoStatus `yaml:"status"`
	ActiveForm string     `yaml:"active_form,omitempty"`
}

// ReadTodos reads the todo list from the session directory.
// Returns an empty slice if the file does not exist.
func ReadTodos(sessionDir string) ([]Todo, error) {
	data, err := os.ReadFile(filepath.Join(sessionDir, todosFileName)) //nolint:gosec // internal session file
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var todos []Todo
	if err := yaml.Unmarshal(data, &todos); err != nil {
		return nil, fmt.Errorf("parsing todos: %w", err)
	}
	return todos, nil
}

// WriteTodos writes the todo list to the session directory.
// Uses atomic write (temp+rename) so concurrent readers never see partial content.
func WriteTodos(sessionDir string, todos []Todo) error {
	data, err := yaml.Marshal(todos)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(sessionDir, todosFileName), data, fsperm.FilePrivate)
}

// FormatTodos renders a todo list as markdown for system prompt injection.
func FormatTodos(todos []Todo) string {
	if len(todos) == 0 {
		return ""
	}
	var b strings.Builder
	for _, t := range todos {
		switch t.Status {
		case TodoStatusCompleted:
			fmt.Fprintf(&b, "- [x] %s\n", t.Content)
		case TodoStatusInProgress:
			fmt.Fprintf(&b, "- [ ] **%s** *(in progress)*\n", t.Content)
		default:
			fmt.Fprintf(&b, "- [ ] %s\n", t.Content)
		}
	}
	return b.String()
}
