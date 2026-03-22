package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const todosFileName = "todos.json"

// TodoStatus represents the state of a todo item.
type TodoStatus string

const (
	TodoStatusPending    TodoStatus = "pending"
	TodoStatusInProgress TodoStatus = "in_progress"
	TodoStatusCompleted  TodoStatus = "completed"
)

// Todo represents a single task in an agent's todo list.
type Todo struct {
	Content    string     `json:"content"`
	Status     TodoStatus `json:"status"`
	ActiveForm string     `json:"active_form,omitempty"`
}

// ReadTodos reads the todo list from the instance directory.
// Returns an empty slice if the file does not exist.
func ReadTodos(instanceDir string) ([]Todo, error) {
	data, err := os.ReadFile(filepath.Join(instanceDir, todosFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var todos []Todo
	if err := json.Unmarshal(data, &todos); err != nil {
		return nil, fmt.Errorf("parsing todos: %w", err)
	}
	return todos, nil
}

// WriteTodos writes the todo list to the instance directory.
func WriteTodos(instanceDir string, todos []Todo) error {
	data, err := json.MarshalIndent(todos, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(instanceDir, todosFileName), data, 0600)
}

// FormatTodos renders a todo list as markdown for system prompt injection.
func FormatTodos(todos []Todo) string {
	if len(todos) == 0 {
		return ""
	}
	var s string
	for _, t := range todos {
		switch t.Status {
		case TodoStatusCompleted:
			s += fmt.Sprintf("- [x] %s\n", t.Content)
		case TodoStatusInProgress:
			s += fmt.Sprintf("- [ ] **%s** *(in progress)*\n", t.Content)
		default:
			s += fmt.Sprintf("- [ ] %s\n", t.Content)
		}
	}
	return s
}
