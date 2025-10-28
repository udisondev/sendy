package chat

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// fileSelectedMsg contains path to selected file
type fileSelectedMsg struct {
	filePath string
	startDir string
	err      error
}

// CreateFzfCommand creates command to launch fzf+fd
func CreateFzfCommand(startDir string) tea.Cmd {
	if startDir == "" {
		startDir, _ = os.UserHomeDir()
	}

	return tea.ExecProcess(createFzfCmd(startDir), func(err error) tea.Msg {
		if err != nil {
			// Exit status 130 means user pressed Esc or Ctrl+C - not an error
			if exitErr, ok := err.(*exec.ExitError); ok {
				if exitErr.ExitCode() == 130 {
					return fileSelectedMsg{startDir: startDir, err: fmt.Errorf("cancelled")}
				}
			}
			return fileSelectedMsg{startDir: startDir, err: err}
		}
		return fileSelectedMsg{startDir: startDir}
	})
}

// createFzfCmd creates exec.Cmd for fzf
func createFzfCmd(startDir string) *exec.Cmd {
	// Create temporary file for result
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("sendychat-file-selection-%d", os.Getpid()))

	// Shell command that launches fd | fzf and saves result to temporary file
	shellCmd := fmt.Sprintf(`
cd %s && \
fd --type f --hidden --exclude .git --exclude node_modules --exclude .DS_Store --color always . | \
fzf --height 80%% --reverse --border \
  --prompt '📁 Select file to send: ' \
  --header 'Tab: toggle preview | Enter: select | Esc: cancel' \
  --preview 'head -n 100 {}' \
  --preview-window 'right:50%%:wrap' \
  --ansi --info inline \
  --bind 'tab:toggle-preview' \
  --bind 'ctrl-/:change-preview-window(down|hidden|)' \
  > %s
`,
		escapeShellArg(startDir),
		escapeShellArg(tmpFile))

	cmd := exec.Command("sh", "-c", shellCmd)
	return cmd
}

// ReadFzfResult читает результат из временного файла
func ReadFzfResult(startDir string) (string, error) {
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("sendychat-file-selection-%d", os.Getpid()))
	defer os.Remove(tmpFile) // Удаляем временный файл

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("cancelled")
		}
		return "", fmt.Errorf("read result: %w", err)
	}

	selectedFile := strings.TrimSpace(string(data))
	if selectedFile == "" {
		return "", fmt.Errorf("no file selected")
	}

	// Если путь относительный, делаем абсолютным
	if !filepath.IsAbs(selectedFile) {
		selectedFile = filepath.Join(startDir, selectedFile)
	}

	return selectedFile, nil
}

// escapeShellArg экранирует аргумент для безопасного использования в shell
func escapeShellArg(arg string) string {
	arg = strings.ReplaceAll(arg, "'", "'\\''")
	return "'" + arg + "'"
}

// CheckFzfInstalled проверяет установлены ли fzf и fd
func CheckFzfInstalled() error {
	if _, err := exec.LookPath("fzf"); err != nil {
		return fmt.Errorf("fzf not installed: install with 'brew install fzf'")
	}
	if _, err := exec.LookPath("fd"); err != nil {
		return fmt.Errorf("fd not installed: install with 'brew install fd'")
	}
	return nil
}
