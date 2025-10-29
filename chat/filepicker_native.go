package chat

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// CreateNativeFilePickerCommand creates a command to launch the OS native file picker
func CreateNativeFilePickerCommand() tea.Cmd {
	return tea.ExecProcess(createNativePickerCmd(), func(err error) tea.Msg {
		if err != nil {
			return fileSelectedMsg{err: err}
		}
		return fileSelectedMsg{}
	})
}

// createNativePickerCmd creates a command depending on the OS
func createNativePickerCmd() *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return createMacOSPickerCmd()
	case "linux":
		return createLinuxPickerCmd()
	case "windows":
		return createWindowsPickerCmd()
	default:
		// Fallback: use simple path input via dialog
		return exec.Command("echo", "")
	}
}

// createMacOSPickerCmd creates an AppleScript command for macOS
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

// createLinuxPickerCmd creates a command for Linux
// Try zenity, kdialog, or yad in order of priority
func createLinuxPickerCmd() *exec.Cmd {
	tmpFile := fmt.Sprintf("/tmp/sendychat-native-selection-%d", getPID())

	// Check which dialog is available
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

	// Fallback: use simple file input
	return exec.Command("echo", "")
}

// createWindowsPickerCmd creates a PowerShell command for Windows
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

// ReadNativePickerResult reads the result from a temporary file
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

// CheckNativePickerAvailable checks if native picker is available
func CheckNativePickerAvailable() bool {
	switch runtime.GOOS {
	case "darwin":
		// macOS always has AppleScript
		return true
	case "linux":
		// Check for dialog tools availability
		_, zenity := exec.LookPath("zenity")
		_, kdialog := exec.LookPath("kdialog")
		_, yad := exec.LookPath("yad")
		return zenity == nil || kdialog == nil || yad == nil
	case "windows":
		// Windows always has PowerShell
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
	defer os.Remove(path) // Remove temporary file
	return os.ReadFile(path)
}
