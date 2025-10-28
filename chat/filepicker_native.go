package chat

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// CreateNativeFilePickerCommand создает команду для запуска нативного file picker'а ОС
func CreateNativeFilePickerCommand() tea.Cmd {
	return tea.ExecProcess(createNativePickerCmd(), func(err error) tea.Msg {
		if err != nil {
			return fileSelectedMsg{err: err}
		}
		return fileSelectedMsg{}
	})
}

// createNativePickerCmd создает команду в зависимости от ОС
func createNativePickerCmd() *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return createMacOSPickerCmd()
	case "linux":
		return createLinuxPickerCmd()
	case "windows":
		return createWindowsPickerCmd()
	default:
		// Fallback: используем simple path input через dialog
		return exec.Command("echo", "")
	}
}

// createMacOSPickerCmd создает AppleScript команду для macOS
func createMacOSPickerCmd() *exec.Cmd {
	tmpFile := fmt.Sprintf("/tmp/sendychat-native-selection-%d", getPID())

	shellCmd := fmt.Sprintf(`osascript -e 'tell application "System Events"
	activate
	set selectedFile to choose file with prompt "Select file to send:" default location (path to home folder)
	set posixPath to POSIX path of selectedFile
	return posixPath
end tell' > %s 2>&1`, tmpFile)

	cmd := exec.Command("sh", "-c", shellCmd)
	return cmd
}

// createLinuxPickerCmd создает команду для Linux
// Пробуем zenity, kdialog, или yad в порядке приоритета
func createLinuxPickerCmd() *exec.Cmd {
	tmpFile := fmt.Sprintf("/tmp/sendychat-native-selection-%d", getPID())

	// Проверяем какой dialog доступен
	if _, err := exec.LookPath("zenity"); err == nil {
		// Zenity (GTK-based)
		cmd := exec.Command("sh", "-c",
			fmt.Sprintf(`zenity --file-selection --title="Select file to send" > %s 2>&1`, tmpFile))
		return cmd
	}

	if _, err := exec.LookPath("kdialog"); err == nil {
		// KDialog (KDE-based)
		cmd := exec.Command("sh", "-c",
			fmt.Sprintf(`kdialog --getopenfilename ~ "All files (*.*)" > %s 2>&1`, tmpFile))
		return cmd
	}

	if _, err := exec.LookPath("yad"); err == nil {
		// YAD (Yet Another Dialog)
		cmd := exec.Command("sh", "-c",
			fmt.Sprintf(`yad --file --title="Select file to send" > %s 2>&1`, tmpFile))
		return cmd
	}

	// Fallback: используем простой file input
	return exec.Command("echo", "")
}

// createWindowsPickerCmd создает PowerShell команду для Windows
func createWindowsPickerCmd() *exec.Cmd {
	tmpFile := fmt.Sprintf(`%s\sendychat-native-selection-%d`, getTempDir(), getPID())

	psScript := `
Add-Type -AssemblyName System.Windows.Forms
$FileBrowser = New-Object System.Windows.Forms.OpenFileDialog
$FileBrowser.Title = "Select file to send"
$FileBrowser.Filter = "All files (*.*)|*.*"
$FileBrowser.InitialDirectory = [Environment]::GetFolderPath("UserProfile")

if ($FileBrowser.ShowDialog() -eq 'OK') {
	$FileBrowser.FileName | Out-File -FilePath '%s' -Encoding utf8
}
`
	psScript = fmt.Sprintf(psScript, tmpFile)

	cmd := exec.Command("powershell", "-Command", psScript)
	return cmd
}

// ReadNativePickerResult читает результат из временного файла
func ReadNativePickerResult() (string, error) {
	tmpFile := fmt.Sprintf("/tmp/sendychat-native-selection-%d", getPID())
	if runtime.GOOS == "windows" {
		tmpFile = fmt.Sprintf(`%s\sendychat-native-selection-%d`, getTempDir(), getPID())
	}

	data, err := readTempFile(tmpFile)
	if err != nil {
		return "", err
	}

	selectedFile := strings.TrimSpace(string(data))
	if selectedFile == "" {
		return "", fmt.Errorf("cancelled")
	}

	return selectedFile, nil
}

// CheckNativePickerAvailable проверяет доступен ли нативный picker
func CheckNativePickerAvailable() bool {
	switch runtime.GOOS {
	case "darwin":
		// macOS всегда имеет AppleScript
		return true
	case "linux":
		// Проверяем наличие dialog tools
		_, zenity := exec.LookPath("zenity")
		_, kdialog := exec.LookPath("kdialog")
		_, yad := exec.LookPath("yad")
		return zenity == nil || kdialog == nil || yad == nil
	case "windows":
		// Windows всегда имеет PowerShell
		return true
	default:
		return false
	}
}

// Helper functions
func getPID() int {
	return os.Getpid()
}

func getTempDir() string {
	return os.TempDir()
}

func readTempFile(path string) ([]byte, error) {
	defer os.Remove(path) // Удаляем временный файл
	return os.ReadFile(path)
}
