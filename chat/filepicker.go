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

// FilePickerModel представляет файловый браузер
type FilePickerModel struct {
	currentDir  string
	entries     []fs.DirEntry
	selected    int
	width       int
	height      int
	onSelect    func(string) // Callback при выборе файла
	onCancel    func()       // Callback при отмене
}

// NewFilePicker создает новый файловый браузер
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

// loadDirectory загружает содержимое текущей директории
func (fp *FilePickerModel) loadDirectory() {
	entries, err := os.ReadDir(fp.currentDir)
	if err != nil {
		// Если не можем прочитать, возвращаемся в домашнюю директорию
		fp.currentDir, _ = os.UserHomeDir()
		entries, _ = os.ReadDir(fp.currentDir)
	}

	// Сортируем: сначала директории, потом файлы
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

// Init инициализирует модель
func (fp *FilePickerModel) Init() tea.Cmd {
	return nil
}

// Update обрабатывает события
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
					// Переходим в директорию
					fp.currentDir = path
					fp.selected = 0
					fp.loadDirectory()
				} else {
					// Выбираем файл
					if fp.onSelect != nil {
						fp.onSelect(path)
					}
				}
			}

		case key.Matches(msg, key.NewBinding(key.WithKeys("backspace", "h"))):
			// Переходим на уровень выше
			parent := filepath.Dir(fp.currentDir)
			if parent != fp.currentDir {
				fp.currentDir = parent
				fp.selected = 0
				fp.loadDirectory()
			}

		case key.Matches(msg, key.NewBinding(key.WithKeys("g"))):
			// Переход в домашнюю директорию
			fp.currentDir, _ = os.UserHomeDir()
			fp.selected = 0
			fp.loadDirectory()
		}
	}

	return fp, nil
}

// View рендерит файловый браузер
func (fp *FilePickerModel) View() string {
	var b strings.Builder

	// Стили
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

	// Заголовок
	b.WriteString(headerStyle.Render("📁 Select File to Send"))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Faint(true).Render(fp.currentDir))
	b.WriteString("\n\n")

	// Показываем ".." для перехода вверх если не в корне
	parent := filepath.Dir(fp.currentDir)
	if parent != fp.currentDir {
		if fp.selected == -1 {
			b.WriteString(selectedStyle.Render("  .. (parent directory)"))
		} else {
			b.WriteString(dirStyle.Render("  .. (parent directory)"))
		}
		b.WriteString("\n")
	}

	// Список файлов и директорий
	visibleStart := 0
	visibleEnd := len(fp.entries)

	// Ограничиваем видимую область если много файлов
	maxVisible := fp.height - 10
	if maxVisible < 5 {
		maxVisible = 5
	}

	if len(fp.entries) > maxVisible {
		// Центрируем выбранный элемент
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

	// Подсказки
	b.WriteString("\n")
	helpStyle := lipgloss.NewStyle().Faint(true)
	b.WriteString(helpStyle.Render("↑/↓: navigate • Enter: select/open • Backspace: parent dir • g: home • Esc: cancel"))

	return b.String()
}
