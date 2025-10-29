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
	"github.com/udisondev/sendy/router"
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
	viewSearch
	viewSearchContacts
)

// model represents TUI state
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
	searchInput         textarea.Model
	searchResults       []*SearchResult
	selectedSearchResult int
	searchContactInput  textarea.Model
	filteredContacts    []*Contact
	selectedFilteredContact int
	jumpToMessageID     int64  // Message ID to scroll to after loading
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

// NewTUI creates a new TUI model
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

	searchInput := textarea.New()
	searchInput.Placeholder = "Search messages..."
	searchInput.Prompt = "> "
	searchInput.CharLimit = 100
	searchInput.SetWidth(70)
	searchInput.SetHeight(1)
	searchInput.ShowLineNumbers = false

	searchContactInput := textarea.New()
	searchContactInput.Placeholder = "Search contacts..."
	searchContactInput.Prompt = "> "
	searchContactInput.CharLimit = 100
	searchContactInput.SetWidth(70)
	searchContactInput.SetHeight(1)
	searchContactInput.ShowLineNumbers = false

	vp := viewport.New(30, 20)

	m := &model{
		chat:               chat,
		myID:               myID,
		mode:               viewMain,
		focus:              focusContacts,
		selectedContact:    0,
		textarea:           ta,
		addContactInput:    addInput,
		renameInput:        renameInput,
		searchInput:        searchInput,
		searchContactInput: searchContactInput,
		viewport:           vp,
		contactsWidth:      30, // Default width for contacts panel
	}

	return m
}

// Init initializes TUI
func (m *model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.loadContacts,
		m.waitForChatEvents,
	)
}

// Update handles messages
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
		case viewSearch:
			return m.updateSearchView(msg)
		case viewSearchContacts:
			return m.updateSearchContactsView(msg)
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
		// Result from fzf file picker
		if msg.err != nil {
			// If cancelled - return immediately without error
			if msg.err.Error() == "cancelled" {
				return m, nil
			}
			// Show other errors
			m.error = fmt.Sprintf("File selection error: %v", msg.err)
			return m, nil
		}

		// Read selected file
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

// View renders UI
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
	case viewSearch:
		return m.viewSearch()
	case viewSearchContacts:
		return m.viewSearchContacts()
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
		helpText = "enter: open chat • ↑/↓: select • /: search contacts • f: send file • a: add • r: rename • d: delete • c: connect • x: disconnect • i: my ID • q: quit"
	case focusMessages:
		helpText = "↑/↓: scroll • /: search messages • tab: next panel"
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

	case "/":
		if m.focus == focusContacts {
			// Search contacts
			m.mode = viewSearchContacts
			m.searchContactInput.Reset()
			m.searchContactInput.Focus()
			m.filteredContacts = nil
			m.selectedFilteredContact = 0
			m.error = ""
			return m, nil
		} else if m.focus == focusMessages {
			// Search messages
			m.mode = viewSearch
			m.searchInput.Reset()
			m.searchInput.Focus()
			m.searchResults = nil
			m.selectedSearchResult = 0
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
		// Open chat with selected contact
		if len(m.contacts) > 0 {
			// Switch focus to message input panel
			m.focus = focusInput
			m.textarea.Focus()
			// Mark messages as read
			contact := m.contacts[m.selectedContact]
			m.chat.MarkAsRead(contact.PeerID)
			// Load messages
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
		// Rename contact
		if len(m.contacts) > 0 {
			m.mode = viewRenameContact
			contact := m.contacts[m.selectedContact]
			m.renameInput.SetValue(contact.Name)
			m.renameInput.Focus()
			m.error = ""
			return m, nil
		}

	case "d":
		// Request deletion confirmation
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

			// Check if fzf+fd is installed
			if err := CheckFzfInstalled(); err == nil {
				// Use fzf+fd
				startDir, _ := os.UserHomeDir()
				return m, CreateFzfCommand(startDir)
			} else {
				// Fallback to built-in file picker
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

		// Generate name from first characters of ID
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
		// Confirmed - delete
		if err := m.chat.DeleteContact(m.contactToDelete); err != nil {
			m.error = err.Error()
			m.mode = viewMain
			return m, nil
		}

		m.mode = viewMain
		m.statusMsg = "Contact deleted"
		return m, m.loadContacts

	case "n", "N", "esc":
		// Cancelled
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
	jumpToLine := -1  // Line to scroll to
	currentLine := 0  // Current line in viewport

	for _, msg := range m.messages {
		// If this is the message to scroll to - remember the line
		if m.jumpToMessageID > 0 && msg.ID == m.jumpToMessageID {
			jumpToLine = currentLine
		}

		timestamp := msg.Timestamp.Format("15:04:05")

		if msg.IsOutgoing {
			line := fmt.Sprintf("[%s] You: %s", timestamp, msg.Content)
			rendered := messageOutgoingStyle.Render(line)
			b.WriteString(rendered + "\n")
			// Count lines (including newlines in Content)
			currentLine += strings.Count(msg.Content, "\n") + 1
		} else {
			line := fmt.Sprintf("[%s] %s", timestamp, msg.Content)
			rendered := messageIncomingStyle.Render(line)
			b.WriteString(rendered + "\n")
			// Count lines (including newlines in Content)
			currentLine += strings.Count(msg.Content, "\n") + 1
		}
	}

	m.viewport.SetContent(b.String())

	// Scroll to the needed message or to the end
	if jumpToLine >= 0 {
		// Scroll to found message
		// Center message in viewport if possible
		targetOffset := jumpToLine - m.viewport.Height/2
		if targetOffset < 0 {
			targetOffset = 0
		}
		m.viewport.SetYOffset(targetOffset)
		m.jumpToMessageID = 0  // Reset flag
	} else {
		m.viewport.GotoBottom()
	}
}

func (m *model) handleChatEvent(event ChatEvent) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch event.Type {
	case ChatEventMessageReceived:
		if m.mode == viewMain && len(m.contacts) > 0 {
			contact := m.contacts[m.selectedContact]
			if contact.PeerID == event.PeerID {
				// Message from selected contact
				// Mark as read
				m.chat.MarkAsRead(event.PeerID)
				// If focus is on contacts, switch to messages
				if m.focus == focusContacts {
					m.focus = focusMessages
				}
				cmd = m.loadMessages
			} else {
				// Message from another contact - update contacts list
				cmd = m.loadContacts
			}
		} else {
			// Update contacts list to show unread messages
			cmd = m.loadContacts
		}

	case ChatEventMessageSent:
		// Message already in history, just reload
		if m.mode == viewMain {
			cmd = m.loadMessages
		}

	case ChatEventContactAdded:
		// New contact added automatically
		m.statusMsg = "New contact added"
		cmd = m.loadContacts

	case ChatEventContactOnline:
		m.statusMsg = "Contact connected"
		cmd = m.loadContacts

	case ChatEventContactOffline:
		m.statusMsg = "Contact disconnected"
		cmd = m.loadContacts

	case ChatEventConnectionFailed:
		// Errors are logged, no need to show in TUI

	case ChatEventError:
		// Errors are logged, no need to show in TUI

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
		cmd = m.loadMessages // Update messages

	case ChatEventFileTransferFailed:
		m.error = fmt.Sprintf("File transfer failed: %v", event.Error)
	}

	// IMPORTANT: always return command to wait for next event
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

	// Mark as read
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

func (m *model) viewSearchContacts() string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("Search Contacts") + "\n\n")
	b.WriteString("  Enter search query:\n\n")
	b.WriteString("  " + m.searchContactInput.View() + "\n\n")

	if len(m.filteredContacts) > 0 {
		b.WriteString(lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("  Found %d contacts:\n\n", len(m.filteredContacts))))

		// Display filtered contacts
		for i, contact := range m.filteredContacts {
			if i >= 20 {
				b.WriteString(statusBarStyle.Render("  ... and more contacts (showing first 20)"))
				break
			}

			style := contactStyle
			if i == m.selectedFilteredContact {
				style = selectedContactStyle
			}

			status := offlineStyle.Render("●")
			if m.chat.IsOnline(contact.PeerID) {
				status = onlineStyle.Render("●")
			}

			blocked := ""
			if contact.IsBlocked {
				blocked = " [Blocked]"
			}

			line := fmt.Sprintf("%s %s%s", status, contact.Name, blocked)
			b.WriteString(style.Render(line) + "\n")
		}
	} else if m.searchContactInput.Value() != "" {
		b.WriteString(statusBarStyle.Render("  No contacts found") + "\n")
	}

	b.WriteString("\n")
	b.WriteString(statusBarStyle.Render("  enter: filter / select contact • ↑/↓ or j/k: navigate • esc: cancel") + "\n")

	if m.error != "" {
		b.WriteString("\n" + errorStyle.Render(m.error))
	}

	return b.String()
}

func (m *model) updateSearchContactsView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg.String() {
	case "esc":
		m.mode = viewMain
		m.searchContactInput.Blur()
		return m, nil

	case "enter":
		// If we have filtered results, select the contact
		if len(m.filteredContacts) > 0 && m.selectedFilteredContact < len(m.filteredContacts) {
			selectedContact := m.filteredContacts[m.selectedFilteredContact]

			// Find contact index in main list
			for i, contact := range m.contacts {
				if contact.PeerID == selectedContact.PeerID {
					m.selectedContact = i
					m.mode = viewMain
					m.focus = focusMessages
					m.searchContactInput.Blur()
					return m, m.loadMessages
				}
			}

			m.error = "Contact not found"
			return m, nil
		}

		// No results yet - perform filter
		query := strings.TrimSpace(m.searchContactInput.Value())
		if query != "" {
			m.filteredContacts = m.filterContacts(query)
			m.selectedFilteredContact = 0
		}
		return m, nil

	case "up", "k":
		if m.selectedFilteredContact > 0 {
			m.selectedFilteredContact--
		}
		return m, nil

	case "down", "j":
		if m.selectedFilteredContact < len(m.filteredContacts)-1 {
			m.selectedFilteredContact++
		}
		return m, nil
	}

	m.searchContactInput, cmd = m.searchContactInput.Update(msg)
	return m, cmd
}

// filterContacts performs case-insensitive substring search on contact names
func (m *model) filterContacts(query string) []*Contact {
	query = strings.ToLower(query)
	var filtered []*Contact

	for _, contact := range m.contacts {
		if strings.Contains(strings.ToLower(contact.Name), query) {
			filtered = append(filtered, contact)
		}
	}

	return filtered
}

// isIgnorableError checks for technical errors that don't need to be shown to user
func isIgnorableError(err error) bool {
	if err == nil {
		return true
	}

	errStr := err.Error()

	// Technical WebRTC/SCTP errors when closing connection
	ignorablePatterns := []string{
		"User Initiated Abort",          // User closed connection
		"abort chunk",                    // SCTP technical detail
		"sending reset packet in non-established state", // Closing already closed connection
	}

	for _, pattern := range ignorablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// makeErrorUserFriendly makes technical errors user-friendly
func makeErrorUserFriendly(err error) string {
	if err == nil {
		return ""
	}

	errStr := err.Error()

	// Connection errors
	if strings.Contains(errStr, "timeout waiting for peer key exchange") {
		return "Unable to connect (peer offline)"
	}
	if strings.Contains(errStr, "Connection failed") {
		return "Connection failed (peer may be offline)"
	}

	// Return original error if no pattern found
	return errStr
}

func (m *model) viewSearch() string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("Search Messages") + "\n\n")
	b.WriteString("  Enter search query:\n\n")
	b.WriteString("  " + m.searchInput.View() + "\n\n")

	if len(m.searchResults) > 0 {
		b.WriteString(lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("  Found %d results:\n\n", len(m.searchResults))))

		// Display search results
		for i, result := range m.searchResults {
			if i >= 20 {
				b.WriteString(statusBarStyle.Render("  ... and more results (showing first 20)"))
				break
			}

			style := contactStyle
			if i == m.selectedSearchResult {
				style = selectedContactStyle
			}

			// Truncate content for preview
			content := result.Content
			if len(content) > 100 {
				content = content[:97] + "..."
			}
			// Replace newlines with spaces for single-line display
			content = strings.ReplaceAll(content, "\n", " ")

			direction := "→"
			if result.IsOutgoing {
				direction = "←"
			}

			timestamp := result.Timestamp.Format("Jan 02 15:04")
			line := fmt.Sprintf("%s [%s] %s: %s", direction, timestamp, result.ContactName, content)
			b.WriteString(style.Render(line) + "\n")
		}
	} else if m.searchInput.Value() != "" {
		b.WriteString(statusBarStyle.Render("  No results found") + "\n")
	}

	b.WriteString("\n")
	b.WriteString(statusBarStyle.Render("  enter: search / jump to message • ↑/↓ or j/k: select result • esc: cancel") + "\n")

	if m.error != "" {
		b.WriteString("\n" + errorStyle.Render(m.error))
	}

	return b.String()
}

func (m *model) updateSearchView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg.String() {
	case "esc":
		m.mode = viewMain
		m.searchInput.Blur()
		return m, nil

	case "enter":
		// If we have results, jump to selected message
		// Otherwise, perform search
		if len(m.searchResults) > 0 && m.selectedSearchResult < len(m.searchResults) {
			result := m.searchResults[m.selectedSearchResult]

			// Find contact index
			for i, contact := range m.contacts {
				if contact.PeerID == result.PeerID {
					m.selectedContact = i
					m.jumpToMessageID = result.ID  // Save ID for scrolling
					m.mode = viewMain
					m.focus = focusMessages
					m.searchInput.Blur()
					return m, m.loadMessages
				}
			}

			m.error = "Contact not found"
			return m, nil
		}

		// No results yet - perform search
		query := strings.TrimSpace(m.searchInput.Value())
		if query != "" {
			results, err := m.chat.SearchMessages(query, 100)
			if err != nil {
				m.error = fmt.Sprintf("Search error: %v", err)
				return m, nil
			}
			m.searchResults = results
			m.selectedSearchResult = 0
		}
		return m, nil

	case "up", "k":
		if m.selectedSearchResult > 0 {
			m.selectedSearchResult--
		}
		return m, nil

	case "down", "j":
		if m.selectedSearchResult < len(m.searchResults)-1 {
			m.selectedSearchResult++
		}
		return m, nil
	}

	m.searchInput, cmd = m.searchInput.Update(msg)
	return m, cmd
}

// RunTUI starts the TUI application
func RunTUI(chat *Chat, myID router.PeerID) error {
	p := tea.NewProgram(
		NewTUI(chat, myID),
		tea.WithAltScreen(),
	)

	_, err := p.Run()
	return err
}
