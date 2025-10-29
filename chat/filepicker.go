package chat

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// FilePickerModel represents a file browser
type FilePickerModel struct {
	currentDir  string
	entries     []fs.DirEntry
	selected    int
	width       int
	height      int
	onSelect    func(string) // Callback when a file is selected
	onCancel    func()       // Callback when cancelled
}

// NewFilePicker creates a new file browser
func NewFilePicker(startDir string, onSelect func(string), onCancel func()) *FilePickerModel {
	if startDir == "" {
		startDir, _ = os.UserHomeDir()
	}

	fp := &FilePickerModel{
		currentDir: startDir,
		selected:   0,
		onSelect:   onSelect,
		onCancel:   onCancel,
	}

	fp.loadDirectory()
	return fp
}

// loadDirectory loads the contents of the current directory
func (fp *FilePickerModel) loadDirectory() {
	entries, err := os.ReadDir(fp.currentDir)
	if err != nil {
		// If unable to read, return to home directory
		fp.currentDir, _ = os.UserHomeDir()
		entries, _ = os.ReadDir(fp.currentDir)
	}

	// Sort: directories first, then files
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() && !entries[j].IsDir() {
			return true
		}
		if !entries[i].IsDir() && entries[j].IsDir() {
			return false
		}
		return entries[i].Name() < entries[j].Name()
	})

	fp.entries = entries
	if fp.selected >= len(fp.entries) {
		fp.selected = len(fp.entries) - 1
	}
	if fp.selected < 0 {
		fp.selected = 0
	}
}

// Init initializes the model
func (fp *FilePickerModel) Init() tea.Cmd {
	return nil
}

// Update handles events
func (fp *FilePickerModel) Update(msg tea.Msg) (*FilePickerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		fp.width = msg.Width
		fp.height = msg.Height

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			if fp.onCancel != nil {
				fp.onCancel()
			}
			return fp, nil

		case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
			if fp.selected > 0 {
				fp.selected--
			}

		case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
			if fp.selected < len(fp.entries)-1 {
				fp.selected++
			}

		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			if len(fp.entries) > 0 {
				entry := fp.entries[fp.selected]
				path := filepath.Join(fp.currentDir, entry.Name())

				if entry.IsDir() {
					// Navigate into directory
					fp.currentDir = path
					fp.selected = 0
					fp.loadDirectory()
				} else {
					// Select file
					if fp.onSelect != nil {
						fp.onSelect(path)
					}
				}
			}

		case key.Matches(msg, key.NewBinding(key.WithKeys("backspace", "h"))):
			// Go up one level
			parent := filepath.Dir(fp.currentDir)
			if parent != fp.currentDir {
				fp.currentDir = parent
				fp.selected = 0
				fp.loadDirectory()
			}

		case key.Matches(msg, key.NewBinding(key.WithKeys("g"))):
			// Go to home directory
			fp.currentDir, _ = os.UserHomeDir()
			fp.selected = 0
			fp.loadDirectory()
		}
	}

	return fp, nil
}

// View renders the file browser
func (fp *FilePickerModel) View() string {
	var b strings.Builder

	// Styles
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7D56F4")).
		Padding(0, 1)

	dirStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#00BFFF")).
		Bold(true)

	fileStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF"))

	selectedStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#7D56F4")).
		Foreground(lipgloss.Color("#FFFFFF")).
		Padding(0, 1)

	// Header
	b.WriteString(headerStyle.Render("📁 Select File to Send"))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Faint(true).Render(fp.currentDir))
	b.WriteString("\n\n")

	// Show ".." for navigating up if not at root
	parent := filepath.Dir(fp.currentDir)
	if parent != fp.currentDir {
		if fp.selected == -1 {
			b.WriteString(selectedStyle.Render("  .. (parent directory)"))
		} else {
			b.WriteString(dirStyle.Render("  .. (parent directory)"))
		}
		b.WriteString("\n")
	}

	// List of files and directories
	visibleStart := 0
	visibleEnd := len(fp.entries)

	// Limit visible area if there are many files
	maxVisible := fp.height - 10
	if maxVisible < 5 {
		maxVisible = 5
	}

	if len(fp.entries) > maxVisible {
		// Center the selected item
		halfVisible := maxVisible / 2
		visibleStart = fp.selected - halfVisible
		if visibleStart < 0 {
			visibleStart = 0
		}
		visibleEnd = visibleStart + maxVisible
		if visibleEnd > len(fp.entries) {
			visibleEnd = len(fp.entries)
			visibleStart = visibleEnd - maxVisible
			if visibleStart < 0 {
				visibleStart = 0
			}
		}
	}

	if visibleStart > 0 {
		b.WriteString(lipgloss.NewStyle().Faint(true).Render(fmt.Sprintf("  ... (%d more above)\n", visibleStart)))
	}

	for i := visibleStart; i < visibleEnd; i++ {
		entry := fp.entries[i]
		name := entry.Name()

		// Get file size if it's a file
		var sizeStr string
		if !entry.IsDir() {
			info, err := entry.Info()
			if err == nil {
				size := info.Size()
				if size < 1024 {
					sizeStr = fmt.Sprintf(" (%d B)", size)
				} else if size < 1024*1024 {
					sizeStr = fmt.Sprintf(" (%.1f KB)", float64(size)/1024)
				} else if size < 1024*1024*1024 {
					sizeStr = fmt.Sprintf(" (%.1f MB)", float64(size)/(1024*1024))
				} else {
					sizeStr = fmt.Sprintf(" (%.1f GB)", float64(size)/(1024*1024*1024))
				}
			}
		}

		var line string
		if entry.IsDir() {
			line = dirStyle.Render("📁 " + name + "/")
		} else {
			line = fileStyle.Render("📄 " + name + sizeStr)
		}

		if i == fp.selected {
			line = selectedStyle.Render("  " + strings.TrimPrefix(line, "  "))
		} else {
			line = "  " + line
		}

		b.WriteString(line)
		b.WriteString("\n")
	}

	if visibleEnd < len(fp.entries) {
		b.WriteString(lipgloss.NewStyle().Faint(true).Render(fmt.Sprintf("  ... (%d more below)\n", len(fp.entries)-visibleEnd)))
	}

	// Hints
	b.WriteString("\n")
	helpStyle := lipgloss.NewStyle().Faint(true)
	b.WriteString(helpStyle.Render("↑/↓: navigate • Enter: select/open • Backspace: parent dir • g: home • Esc: cancel"))

	return b.String()
}
