package chat

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"sendy/router"
)

// Focus panels
type focusPanel int

const (
	focusContacts focusPanel = iota
	focusMessages
	focusInput
)

// View modes
type viewMode int

const (
	viewMain viewMode = iota
	viewAddContact
	viewShowMyID
	viewRenameContact
	viewConfirmDelete
	viewFilePicker
)

// model представляет состояние TUI
type model struct {
	chat                *Chat
	myID                router.PeerID
	mode                viewMode
	focus               focusPanel
	contacts            []*Contact
	selectedContact     int
	messages            []*Message
	viewport            viewport.Model
	textarea            textarea.Model
	addContactInput     textarea.Model
	renameInput         textarea.Model
	filePicker          *FilePickerModel
	width               int
	height              int
	ready               bool
	statusMsg           string
	error               string
	contactsWidth       int
	contactToDelete     router.PeerID
	contactToDeleteName string
}

// Styles
var (
	// Panel borders
	activeBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("62"))

	inactiveBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("240"))

	// Contacts panel
	contactsPanelStyle = lipgloss.NewStyle().
				Padding(0, 1)

	contactStyle = lipgloss.NewStyle().
			Padding(0, 1)

	selectedContactStyle = lipgloss.NewStyle().
				Padding(0, 1).
				Background(lipgloss.Color("62")).
				Foreground(lipgloss.Color("230")).
				Bold(true)

	// Status indicators
	onlineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("10"))

	offlineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8"))

	// Messages
	messageOutgoingStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("12"))

	messageIncomingStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("10"))

	messageTimeStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("8"))

	// Header
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			Padding(0, 1)

	// Status bar
	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")).
			Padding(0, 1)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")).
			Bold(true).
			Padding(0, 1)
)

// NewTUI создает новую TUI модель
func NewTUI(chat *Chat, myID router.PeerID) *model {
	ta := textarea.New()
	ta.Placeholder = "Type a message... (Ctrl+S to send)"
	ta.Prompt = "│ "
	ta.CharLimit = 1000
	ta.SetWidth(30)
	ta.SetHeight(3)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetEnabled(true)
	ta.Blur() // Start unfocused

	addInput := textarea.New()
	addInput.Placeholder = "Enter peer ID (hex)..."
	addInput.Prompt = "> "
	addInput.CharLimit = 64
	addInput.SetWidth(70)
	addInput.SetHeight(1)
	addInput.ShowLineNumbers = false

	renameInput := textarea.New()
	renameInput.Placeholder = "Enter new name..."
	renameInput.Prompt = "> "
	renameInput.CharLimit = 50
	renameInput.SetWidth(50)
	renameInput.SetHeight(1)
	renameInput.ShowLineNumbers = false

	vp := viewport.New(30, 20)

	m := &model{
		chat:            chat,
		myID:            myID,
		mode:            viewMain,
		focus:           focusContacts,
		selectedContact: 0,
		textarea:        ta,
		addContactInput: addInput,
		renameInput:     renameInput,
		viewport:        vp,
		contactsWidth:   30, // Default width for contacts panel
	}

	return m
}

// Init инициализирует TUI
func (m *model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.loadContacts,
		m.waitForChatEvents,
	)
}

// Update обрабатывает сообщения
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Chat area width (minus contacts panel and borders)
		chatWidth := msg.Width - m.contactsWidth - 4

		if !m.ready {
			m.viewport = viewport.New(chatWidth-4, msg.Height-11) // Adjusted for new layout
			m.viewport.YPosition = 0
			m.textarea.SetWidth(chatWidth - 4)
			m.ready = true
		} else {
			m.viewport.Width = chatWidth - 4
			m.viewport.Height = msg.Height - 11
			m.textarea.SetWidth(chatWidth - 4)
		}

	case tea.KeyMsg:
		switch m.mode {
		case viewMain:
			return m.updateMainView(msg)
		case viewAddContact:
			return m.updateAddContactView(msg)
		case viewShowMyID:
			return m.updateShowMyIDView(msg)
		case viewRenameContact:
			return m.updateRenameContactView(msg)
		case viewConfirmDelete:
			return m.updateConfirmDeleteView(msg)
		case viewFilePicker:
			return m.updateFilePickerView(msg)
		}

	case contactsLoadedMsg:
		m.contacts = msg.contacts
		if len(m.contacts) > 0 && m.selectedContact >= len(m.contacts) {
			m.selectedContact = len(m.contacts) - 1
		}

	case messagesLoadedMsg:
		m.messages = msg.messages
		m.updateViewport()

	case chatEventMsg:
		return m.handleChatEvent(msg.event)

	case statusMsg:
		m.statusMsg = string(msg)
		m.error = ""

	case errorMsg:
		m.error = string(msg)
		m.statusMsg = ""

	case fileSelectedMsg:
		// Результат от fzf file picker
		if msg.err != nil {
			// Если отменено - сразу возвращаемся без ошибки
			if msg.err.Error() == "cancelled" {
				return m, nil
			}
			// Другие ошибки показываем
			m.error = fmt.Sprintf("File selection error: %v", msg.err)
			return m, nil
		}

		// Читаем выбранный файл
		filePath, err := ReadFzfResult(msg.startDir)
		if err != nil {
			if err.Error() != "cancelled" {
				m.error = fmt.Sprintf("Failed to read selection: %v", err)
			}
			return m, nil
		}

		// Send file to selected contact
		if len(m.contacts) > 0 {
			contact := m.contacts[m.selectedContact]
			if err := m.chat.SendFile(contact.PeerID, filePath); err != nil {
				m.error = fmt.Sprintf("Failed to send file: %v", err)
			} else {
				m.statusMsg = "Sending file..."
			}
		}
		return m, nil
	}

	return m, tea.Batch(cmds...)
}

// View рендерит UI
func (m *model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	switch m.mode {
	case viewMain:
		return m.viewMain()
	case viewAddContact:
		return m.viewAddContact()
	case viewShowMyID:
		return m.viewShowMyID()
	case viewRenameContact:
		return m.viewRenameContact()
	case viewConfirmDelete:
		return m.viewConfirmDelete()
	case viewFilePicker:
		return m.viewFilePicker()
	}

	return ""
}

func (m *model) viewMain() string {
	// Left panel: contacts list
	contactsPanel := m.renderContactsPanel()

	// Right panel: chat window
	chatPanel := m.renderChatPanel()

	// Combine panels side by side
	mainView := lipgloss.JoinHorizontal(
		lipgloss.Top,
		contactsPanel,
		chatPanel,
	)

	// Status bar at bottom
	statusBar := m.renderStatusBar()

	return lipgloss.JoinVertical(lipgloss.Left, mainView, statusBar)
}

func (m *model) renderContactsPanel() string {
	var b strings.Builder

	contactsHeight := m.height - 3 // Minus header and status bar

	// Header
	b.WriteString(headerStyle.Render("Contacts") + "\n")

	if len(m.contacts) == 0 {
		b.WriteString(statusBarStyle.Render("No contacts. Press 'a' to add.") + "\n")
	} else {
		// Render contacts list
		for i, contact := range m.contacts {
			if i >= contactsHeight-2 {
				break // Don't overflow
			}

			style := contactStyle
			if i == m.selectedContact {
				style = selectedContactStyle
			}

			status := offlineStyle.Render("●")
			if m.chat.IsOnline(contact.PeerID) {
				status = onlineStyle.Render("●")
			}

			unread, _ := m.chat.GetUnreadCount(contact.PeerID)
			unreadStr := ""
			if unread > 0 {
				unreadStr = fmt.Sprintf(" (%d)", unread)
			}

			blocked := ""
			if contact.IsBlocked {
				blocked = " [X]"
			}

			// Truncate name if too long
			name := contact.Name
			maxNameLen := m.contactsWidth - 7 // Status + padding
			if len(name) > maxNameLen {
				name = name[:maxNameLen-3] + "..."
			}

			line := fmt.Sprintf("%s %s%s%s", status, name, unreadStr, blocked)
			b.WriteString(style.Render(line) + "\n")
		}
	}

	content := b.String()

	// Apply border based on focus
	borderStyle := inactiveBorderStyle
	if m.focus == focusContacts {
		borderStyle = activeBorderStyle
	}

	return borderStyle.Width(m.contactsWidth).Height(m.height - 2).Render(content)
}

func (m *model) renderChatPanel() string {
	chatWidth := m.width - m.contactsWidth - 4

	if len(m.contacts) == 0 || m.selectedContact >= len(m.contacts) {
		emptyMsg := statusBarStyle.Render("No contact selected")
		return inactiveBorderStyle.
			Width(chatWidth).
			Height(m.height - 2).
			Render(emptyMsg)
	}

	contact := m.contacts[m.selectedContact]

	var b strings.Builder

	// Header with contact name and status
	status := offlineStyle.Render("[Offline]")
	if m.chat.IsOnline(contact.PeerID) {
		status = onlineStyle.Render("[Online]")
	}

	header := fmt.Sprintf("%s %s", contact.Name, status)
	b.WriteString(headerStyle.Render(header) + "\n")

	// Messages viewport
	messagesIndicator := "Messages"
	if m.focus == focusMessages {
		messagesIndicator = "Messages [active]"
	}
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(messagesIndicator) + "\n")
	b.WriteString(strings.Repeat("─", chatWidth-4) + "\n")

	// Viewport content (without inner border)
	viewportHeight := m.height - 11 // Header + messages label + separator + input area + status
	m.viewport.Height = viewportHeight
	b.WriteString(m.viewport.View() + "\n")

	b.WriteString(strings.Repeat("─", chatWidth-4) + "\n")

	// Input area indicator
	inputIndicator := "Input"
	if m.focus == focusInput {
		inputIndicator = "Input [active]"
	}
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(inputIndicator) + "\n")
	b.WriteString(m.textarea.View())

	content := b.String()

	// Apply outer border to entire chat panel
	borderStyle := inactiveBorderStyle
	if m.focus == focusMessages || m.focus == focusInput {
		borderStyle = activeBorderStyle
	}

	return borderStyle.Width(chatWidth).Height(m.height - 2).Render(content)
}

func (m *model) renderStatusBar() string {
	var helpText string

	switch m.focus {
	case focusContacts:
		helpText = "enter: open chat • ↑/↓: select • f: send file • a: add • r: rename • d: delete • c: connect • x: disconnect • i: my ID • q: quit"
	case focusMessages:
		helpText = "↑/↓: scroll • tab: next panel"
	case focusInput:
		helpText = "enter: send • tab: next panel"
	}

	status := statusBarStyle.Render(helpText)

	if m.error != "" {
		status = errorStyle.Render("Error: " + m.error)
	} else if m.statusMsg != "" {
		status = statusBarStyle.Render(m.statusMsg)
	}

	return status
}

func (m *model) updateMainView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	// Global keys (work in any panel)
	switch msg.String() {
	case "ctrl+c", "q":
		if m.focus == focusInput && m.textarea.Focused() {
			// Don't quit when typing
		} else {
			return m, tea.Quit
		}

	case "tab":
		// Cycle through panels
		m.focus = (m.focus + 1) % 3

		// Update focus states
		if m.focus == focusInput {
			m.textarea.Focus()
		} else {
			m.textarea.Blur()
		}
		return m, nil

	case "a":
		if m.focus == focusContacts {
			m.mode = viewAddContact
			m.addContactInput.Reset()
			m.addContactInput.Focus()
			m.error = ""
			return m, nil
		}

	case "i":
		if m.focus == focusContacts {
			m.mode = viewShowMyID
			m.error = ""
			return m, nil
		}
	}

	// Panel-specific keys
	switch m.focus {
	case focusContacts:
		return m.updateContactsFocus(msg)
	case focusMessages:
		return m.updateMessagesFocus(msg)
	case focusInput:
		m.textarea, cmd = m.textarea.Update(msg)
		return m.updateInputFocus(msg, cmd)
	}

	return m, nil
}

func (m *model) viewAddContact() string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("Add Contact") + "\n\n")
	b.WriteString("  Enter peer ID (64 hex characters):\n\n")
	b.WriteString("  " + m.addContactInput.View() + "\n\n")
	b.WriteString(statusBarStyle.Render("  enter: add • esc: cancel") + "\n")

	if m.error != "" {
		b.WriteString("\n" + errorStyle.Render(m.error))
	}

	return b.String()
}

func (m *model) viewShowMyID() string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("My ID") + "\n\n")
	hexID := hex.EncodeToString(m.myID[:])
	b.WriteString("  " + hexID + "\n\n")
	b.WriteString(statusBarStyle.Render("  Share this ID with others to let them connect to you") + "\n\n")
	b.WriteString(statusBarStyle.Render("  press any key to go back") + "\n")

	return b.String()
}

// Helper methods

func (m *model) updateContactsFocus(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		// Открываем чат с выбранным контактом
		if len(m.contacts) > 0 {
			// Переключаем фокус на панель ввода сообщения
			m.focus = focusInput
			m.textarea.Focus()
			// Отмечаем сообщения как прочитанные
			contact := m.contacts[m.selectedContact]
			m.chat.MarkAsRead(contact.PeerID)
			// Загружаем сообщения
			return m, m.loadMessages
		}

	case "up", "k":
		if m.selectedContact > 0 {
			m.selectedContact--
			// Load messages for newly selected contact
			return m, m.loadMessages
		}

	case "down", "j":
		if m.selectedContact < len(m.contacts)-1 {
			m.selectedContact++
			// Load messages for newly selected contact
			return m, m.loadMessages
		}

	case "r":
		// Переименовать контакт
		if len(m.contacts) > 0 {
			m.mode = viewRenameContact
			contact := m.contacts[m.selectedContact]
			m.renameInput.SetValue(contact.Name)
			m.renameInput.Focus()
			m.error = ""
			return m, nil
		}

	case "d":
		// Запросить подтверждение удаления
		if len(m.contacts) > 0 {
			contact := m.contacts[m.selectedContact]
			m.contactToDelete = contact.PeerID
			m.contactToDeleteName = contact.Name
			m.mode = viewConfirmDelete
			m.error = ""
			return m, nil
		}

	case "b":
		if len(m.contacts) > 0 {
			contact := m.contacts[m.selectedContact]
			if contact.IsBlocked {
				if err := m.chat.UnblockContact(contact.PeerID); err != nil {
					m.error = err.Error()
				} else {
					m.statusMsg = "Contact unblocked"
					return m, m.loadContacts
				}
			} else {
				if err := m.chat.BlockContact(contact.PeerID); err != nil {
					m.error = err.Error()
				} else {
					m.statusMsg = "Contact blocked"
					return m, m.loadContacts
				}
			}
		}

	case "c":
		// Connect to selected contact
		if len(m.contacts) > 0 {
			contact := m.contacts[m.selectedContact]
			hexID := hex.EncodeToString(contact.PeerID[:])
			if err := m.chat.Connect(hexID); err != nil {
				m.error = err.Error()
			} else {
				m.statusMsg = "Connecting..."
			}
		}

	case "x":
		// Disconnect from selected contact
		if len(m.contacts) > 0 {
			contact := m.contacts[m.selectedContact]
			if err := m.chat.Disconnect(contact.PeerID); err != nil {
				m.error = err.Error()
			} else {
				m.statusMsg = "Disconnected"
			}
		}

	case "f":
		// Open file picker to send file
		if len(m.contacts) > 0 {
			contact := m.contacts[m.selectedContact]
			if !m.chat.IsOnline(contact.PeerID) {
				m.error = "Contact is offline"
				return m, nil
			}

			// Проверяем установлен ли fzf+fd
			if err := CheckFzfInstalled(); err == nil {
				// Используем fzf+fd
				startDir, _ := os.UserHomeDir()
				return m, CreateFzfCommand(startDir)
			} else {
				// Fallback на встроенный file picker
				m.filePicker = NewFilePicker("",
					func(filePath string) {
						// File selected - send it
						if err := m.chat.SendFile(contact.PeerID, filePath); err != nil {
							m.error = fmt.Sprintf("Failed to send file: %v", err)
						} else {
							m.statusMsg = "Sending file..."
						}
						m.mode = viewMain
						m.filePicker = nil
					},
					func() {
						// Cancelled
						m.mode = viewMain
						m.filePicker = nil
					},
				)
				m.mode = viewFilePicker
				return m, nil
			}
		}
	}

	return m, nil
}

func (m *model) updateMessagesFocus(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg.String() {
	case "up", "k":
		m.viewport.LineUp(1)

	case "down", "j":
		m.viewport.LineDown(1)

	case "pgup":
		m.viewport.ViewUp()

	case "pgdown":
		m.viewport.ViewDown()
	}

	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *model) updateInputFocus(msg tea.KeyMsg, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+s":
		if len(m.contacts) > 0 {
			content := strings.TrimSpace(m.textarea.Value())
			if content != "" {
				contact := m.contacts[m.selectedContact]
				if err := m.chat.SendMessage(contact.PeerID, content); err != nil {
					m.error = err.Error()
				} else {
					m.textarea.Reset()
					return m, m.loadMessages
				}
			}
		}
		return m, nil
	}

	return m, cmd
}

func (m *model) updateAddContactView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg.String() {
	case "esc":
		m.mode = viewMain
		m.addContactInput.Blur()
		return m, nil

	case "enter":
		hexID := strings.TrimSpace(m.addContactInput.Value())
		if len(hexID) != 64 {
			m.error = "Peer ID must be exactly 64 hex characters"
			return m, nil
		}

		// Генерируем имя из первых символов ID
		name := "Peer-" + hexID[:8]

		if err := m.chat.AddContact(hexID, name); err != nil {
			m.error = err.Error()
			return m, nil
		}

		m.mode = viewMain
		m.statusMsg = "Contact added"
		m.addContactInput.Blur()
		return m, m.loadContacts
	}

	m.addContactInput, cmd = m.addContactInput.Update(msg)
	return m, cmd
}

func (m *model) updateShowMyIDView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = viewMain
	return m, nil
}

func (m *model) viewRenameContact() string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("Rename Contact") + "\n\n")
	b.WriteString("  Enter new name:\n\n")
	b.WriteString("  " + m.renameInput.View() + "\n\n")
	b.WriteString(statusBarStyle.Render("  enter: save • esc: cancel") + "\n")

	if m.error != "" {
		b.WriteString("\n" + errorStyle.Render(m.error))
	}

	return b.String()
}

func (m *model) updateRenameContactView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg.String() {
	case "esc":
		m.mode = viewMain
		m.renameInput.Blur()
		return m, nil

	case "enter":
		newName := strings.TrimSpace(m.renameInput.Value())
		if newName == "" {
			m.error = "Name cannot be empty"
			return m, nil
		}

		if len(m.contacts) > 0 {
			contact := m.contacts[m.selectedContact]
			if err := m.chat.RenameContact(contact.PeerID, newName); err != nil {
				m.error = err.Error()
				return m, nil
			}

			m.mode = viewMain
			m.statusMsg = "Contact renamed"
			m.renameInput.Blur()
			return m, m.loadContacts
		}
	}

	m.renameInput, cmd = m.renameInput.Update(msg)
	return m, cmd
}

func (m *model) viewConfirmDelete() string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("Delete Contact") + "\n\n")
	b.WriteString(fmt.Sprintf("  Are you sure you want to delete '%s'?\n\n", m.contactToDeleteName))
	b.WriteString(errorStyle.Render("  This will delete all messages with this contact!") + "\n\n")
	b.WriteString(statusBarStyle.Render("  y: yes, delete • n: no, cancel") + "\n")

	return b.String()
}

func (m *model) updateConfirmDeleteView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		// Подтверждено - удаляем
		if err := m.chat.DeleteContact(m.contactToDelete); err != nil {
			m.error = err.Error()
			m.mode = viewMain
			return m, nil
		}

		m.mode = viewMain
		m.statusMsg = "Contact deleted"
		return m, m.loadContacts

	case "n", "N", "esc":
		// Отменено
		m.mode = viewMain
		return m, nil
	}

	return m, nil
}

func (m *model) viewFilePicker() string {
	if m.filePicker == nil {
		return "File picker not initialized"
	}
	return m.filePicker.View()
}

func (m *model) updateFilePickerView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filePicker == nil {
		m.mode = viewMain
		return m, nil
	}

	// Update file picker with the key message
	updatedPicker, cmd := m.filePicker.Update(msg)
	m.filePicker = updatedPicker

	return m, cmd
}

func (m *model) updateViewport() {
	var b strings.Builder

	for _, msg := range m.messages {
		timestamp := msg.Timestamp.Format("15:04:05")

		if msg.IsOutgoing {
			line := fmt.Sprintf("[%s] You: %s", timestamp, msg.Content)
			b.WriteString(messageOutgoingStyle.Render(line) + "\n")
		} else {
			line := fmt.Sprintf("[%s] %s", timestamp, msg.Content)
			b.WriteString(messageIncomingStyle.Render(line) + "\n")
		}
	}

	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}

func (m *model) handleChatEvent(event ChatEvent) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch event.Type {
	case ChatEventMessageReceived:
		if m.mode == viewMain && len(m.contacts) > 0 {
			contact := m.contacts[m.selectedContact]
			if contact.PeerID == event.PeerID {
				// Сообщение от выбранного контакта
				// Отмечаем как прочитанное
				m.chat.MarkAsRead(event.PeerID)
				// Если фокус на контактах, переключаем на сообщения
				if m.focus == focusContacts {
					m.focus = focusMessages
				}
				cmd = m.loadMessages
			} else {
				// Сообщение от другого контакта - обновляем список контактов
				cmd = m.loadContacts
			}
		} else {
			// Обновляем список контактов чтобы показать непрочитанные
			cmd = m.loadContacts
		}

	case ChatEventMessageSent:
		// Сообщение уже в истории, просто перезагружаем
		if m.mode == viewMain {
			cmd = m.loadMessages
		}

	case ChatEventContactAdded:
		// Новый контакт добавлен автоматически
		m.statusMsg = "New contact added"
		cmd = m.loadContacts

	case ChatEventContactOnline:
		m.statusMsg = "Contact connected"
		cmd = m.loadContacts

	case ChatEventContactOffline:
		m.statusMsg = "Contact disconnected"
		cmd = m.loadContacts

	case ChatEventConnectionFailed:
		m.error = fmt.Sprintf("Connection failed: %v", event.Error)

	case ChatEventError:
		m.error = fmt.Sprintf("Error: %v", event.Error)

	case ChatEventFileTransferStarted:
		if event.FileTransfer.IsOutgoing {
			m.statusMsg = fmt.Sprintf("Sending file: %s", event.FileTransfer.FileName)
		} else {
			m.statusMsg = fmt.Sprintf("Receiving file: %s", event.FileTransfer.FileName)
		}

	case ChatEventFileTransferProgress:
		if event.FileTransfer.IsOutgoing {
			m.statusMsg = fmt.Sprintf("Sending %s: %d%%", event.FileTransfer.FileName, event.FileTransfer.Progress)
		} else {
			m.statusMsg = fmt.Sprintf("Receiving %s: %d%%", event.FileTransfer.FileName, event.FileTransfer.Progress)
		}

	case ChatEventFileTransferCompleted:
		if event.FileTransfer.IsOutgoing {
			m.statusMsg = fmt.Sprintf("File sent: %s", event.FileTransfer.FileName)
		} else {
			m.statusMsg = fmt.Sprintf("File received: %s → %s", event.FileTransfer.FileName, event.FileTransfer.FilePath)
		}
		cmd = m.loadMessages // Обновляем сообщения

	case ChatEventFileTransferFailed:
		m.error = fmt.Sprintf("File transfer failed: %v", event.Error)
	}

	// ВАЖНО: всегда возвращаем команду для ожидания следующего события
	return m, tea.Batch(cmd, m.waitForChatEvents)
}

// Commands

type contactsLoadedMsg struct {
	contacts []*Contact
}

func (m *model) loadContacts() tea.Msg {
	contacts, err := m.chat.GetContacts()
	if err != nil {
		return errorMsg(err.Error())
	}
	return contactsLoadedMsg{contacts}
}

type messagesLoadedMsg struct {
	messages []*Message
}

func (m *model) loadMessages() tea.Msg {
	if len(m.contacts) == 0 || m.selectedContact >= len(m.contacts) {
		return messagesLoadedMsg{nil}
	}

	contact := m.contacts[m.selectedContact]
	messages, err := m.chat.GetMessages(contact.PeerID, 100)
	if err != nil {
		return errorMsg(err.Error())
	}

	// Отмечаем как прочитанное
	m.chat.MarkAsRead(contact.PeerID)

	return messagesLoadedMsg{messages}
}

type chatEventMsg struct {
	event ChatEvent
}

func (m *model) waitForChatEvents() tea.Msg {
	event := <-m.chat.Events()
	return chatEventMsg{event}
}

type statusMsg string
type errorMsg string

// RunTUI запускает TUI приложение
func RunTUI(chat *Chat, myID router.PeerID) error {
	p := tea.NewProgram(
		NewTUI(chat, myID),
		tea.WithAltScreen(),
	)

	_, err := p.Run()
	return err
}
