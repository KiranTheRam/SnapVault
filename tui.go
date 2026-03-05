package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tuiStep int

const (
	stepSelectShares tuiStep = iota
	stepAddShare
	stepSelectMount
	stepManualMount
	stepPhotoshoot
	stepTransfer
	stepSummary
	stepDone
)

type smbValidationResultMsg struct {
	share SMBConfig
	err   error
}

type transferStartedMsg struct {
	total      int
	folderName string
}

type transferProgressMsg struct {
	total     int
	completed int
	filePath  string
}

type transferFinishedMsg struct {
	err    error
	errors []TransferError
}

type transferChannelClosedMsg struct{}

type snapVaultTUI struct {
	configPath string
	configData *Config

	step tuiStep

	width  int
	height int

	availableShares []SMBConfig
	selectedShares  map[string]bool
	shareCursor     int

	mountCandidates []MountCandidate
	mountCursor     int

	addShareInputs []textinput.Model
	addShareFocus  int
	isValidating   bool

	manualMountInput textinput.Model
	photoshootInput  textinput.Model

	statusMessage string
	setupError    error

	mountDefault string
	nameDefault  string
	timeout      time.Duration
	workers      int

	resultMount string
	resultName  string
	folderName  string

	progressBar       progress.Model
	transferTotal     int
	transferCompleted int
	transferFile      string
	transferStartedAt time.Time
	transferEndedAt   time.Time
	transferErrors    []TransferError
	transferErr       error

	transferEvents chan tea.Msg
	transferCancel context.CancelFunc
}

var (
	frameStyle = lipgloss.NewStyle().Padding(1, 2)
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.Border{
			Top:         "-",
			Bottom:      "-",
			Left:        "|",
			Right:       "|",
			TopLeft:     "+",
			TopRight:    "+",
			BottomLeft:  "+",
			BottomRight: "+",
		}).
		Padding(0, 1)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

func runInteractiveTUI(configPath, mountDefault, nameDefault string, timeout time.Duration, workers int) error {
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(oldLogger)

	configData, loadErr := loadConfigRaw(configPath)
	if loadErr != nil {
		configData = &Config{}
	}

	model := newSnapVaultTUI(configPath, configData, mountDefault, nameDefault, timeout, workers, loadErr)
	program := tea.NewProgram(model, tea.WithAltScreen())

	finalModel, err := program.Run()
	if err != nil {
		return err
	}

	m, ok := finalModel.(*snapVaultTUI)
	if !ok {
		return errors.New("unexpected TUI model result")
	}
	if m.setupError != nil {
		return m.setupError
	}
	if m.transferErr != nil {
		return m.transferErr
	}
	if len(m.transferErrors) > 0 {
		return fmt.Errorf("transfer completed with %d errors", len(m.transferErrors))
	}
	if m.step != stepDone {
		return errors.New("interactive session did not complete")
	}

	return nil
}

func newSnapVaultTUI(
	configPath string,
	configData *Config,
	mountDefault, nameDefault string,
	timeout time.Duration,
	workers int,
	loadErr error,
) *snapVaultTUI {
	hostInput := textinput.New()
	hostInput.Prompt = "NAS URL/IP: "
	hostInput.Placeholder = "192.168.1.33 or 192.168.1.33:445"

	sharePathInput := textinput.New()
	sharePathInput.Prompt = "Share path: "
	sharePathInput.Placeholder = "general/snapvault"

	usernameInput := textinput.New()
	usernameInput.Prompt = "Username: "
	usernameInput.Placeholder = "kiran"

	passwordInput := textinput.New()
	passwordInput.Prompt = "Password: "
	passwordInput.EchoMode = textinput.EchoPassword
	passwordInput.EchoCharacter = '*'

	mountInput := textinput.New()
	mountInput.Prompt = "Mount path: "
	mountInput.Placeholder = "/media/kiran/SDCARD"
	mountInput.SetValue(strings.TrimSpace(mountDefault))

	nameInput := textinput.New()
	nameInput.Prompt = "Photoshoot name: "
	nameInput.Placeholder = "Wedding"
	nameInput.SetValue(strings.TrimSpace(nameDefault))

	p := progress.New(progress.WithDefaultGradient())
	p.Width = 40

	model := &snapVaultTUI{
		configPath:       configPath,
		configData:       configData,
		step:             stepSelectShares,
		width:            120,
		height:           36,
		availableShares:  append([]SMBConfig(nil), configData.SMBShares...),
		selectedShares:   make(map[string]bool),
		mountCandidates:  detectMountCandidates(),
		addShareInputs:   []textinput.Model{hostInput, sharePathInput, usernameInput, passwordInput},
		manualMountInput: mountInput,
		photoshootInput:  nameInput,
		mountDefault:     strings.TrimSpace(mountDefault),
		nameDefault:      strings.TrimSpace(nameDefault),
		timeout:          timeout,
		workers:          workers,
		progressBar:      p,
	}

	model.addShareInputs[0].Focus()
	model.manualMountInput.Blur()
	model.photoshootInput.Blur()

	if loadErr != nil {
		model.statusMessage = fmt.Sprintf("No existing config loaded (%v). Add a NAS connection.", loadErr)
	}

	return model
}

func (m *snapVaultTUI) Init() tea.Cmd {
	return nil
}

func (m *snapVaultTUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncInputWidths()

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			if m.transferCancel != nil {
				m.transferCancel()
			}
			m.setupError = errors.New("interactive session canceled")
			return m, tea.Quit
		}

	case smbValidationResultMsg:
		m.isValidating = false
		if msg.err != nil {
			m.statusMessage = fmt.Sprintf("Connection failed: %v", msg.err)
			m.addShareFocus = 0
			m.focusAddShareInput(0)
			return m, nil
		}

		m.availableShares = upsertShareConfig(m.availableShares, msg.share)
		m.configData.SMBShares = upsertShareConfig(m.configData.SMBShares, msg.share)

		saveErr := saveConfig(m.configPath, m.configData)
		if saveErr != nil {
			m.statusMessage = fmt.Sprintf("Connected, but failed to save config: %v", saveErr)
		} else {
			m.statusMessage = fmt.Sprintf("Connected to %s and saved.", formatShareForDisplay(msg.share))
		}

		m.selectedShares[smbShareKey(msg.share)] = true
		m.resetAddShareForm()
		m.step = stepSelectShares
		m.shareCursor = len(m.availableShares) + 1
		return m, nil

	case transferStartedMsg:
		if msg.folderName != "" {
			m.folderName = msg.folderName
		}
		if msg.total > 0 {
			m.transferTotal = msg.total
		}
		cmds = append(cmds, waitForTransferMsg(m.transferEvents))

	case transferProgressMsg:
		m.transferTotal = msg.total
		m.transferCompleted = msg.completed
		m.transferFile = filepath.Base(msg.filePath)
		percent := progressPercent(m.transferCompleted, m.transferTotal)
		cmds = append(cmds, m.progressBar.SetPercent(percent))
		cmds = append(cmds, waitForTransferMsg(m.transferEvents))

	case transferFinishedMsg:
		m.transferEndedAt = time.Now()
		m.transferErr = msg.err
		m.transferErrors = msg.errors
		m.transferCancel = nil
		m.step = stepSummary
		if msg.err != nil {
			m.statusMessage = fmt.Sprintf("Transfer failed: %v", msg.err)
		} else if len(msg.errors) > 0 {
			m.statusMessage = fmt.Sprintf("Transfer completed with %d errors.", len(msg.errors))
		} else {
			m.statusMessage = "Transfer completed successfully."
		}

	case transferChannelClosedMsg:
		if m.step == stepTransfer && m.transferErr == nil {
			m.transferErr = errors.New("transfer channel closed unexpectedly")
			m.step = stepSummary
		}
	}

	switch m.step {
	case stepSelectShares:
		model, cmd := m.updateSelectShares(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return model, tea.Batch(cmds...)
	case stepAddShare:
		model, cmd := m.updateAddShare(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return model, tea.Batch(cmds...)
	case stepSelectMount:
		model, cmd := m.updateSelectMount(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return model, tea.Batch(cmds...)
	case stepManualMount:
		model, cmd := m.updateManualMount(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return model, tea.Batch(cmds...)
	case stepPhotoshoot:
		model, cmd := m.updatePhotoshoot(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return model, tea.Batch(cmds...)
	case stepTransfer:
		updatedModel, progressCmd := m.progressBar.Update(msg)
		if pm, ok := updatedModel.(progress.Model); ok {
			m.progressBar = pm
		}
		if progressCmd != nil {
			cmds = append(cmds, progressCmd)
		}
		return m, tea.Batch(cmds...)
	case stepSummary:
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			switch keyMsg.String() {
			case "enter", "esc":
				m.step = stepDone
				return m, tea.Quit
			}
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *snapVaultTUI) View() string {
	leftWidth := max(34, m.width/3)
	rightWidth := max(56, m.width-leftWidth-7)

	summary := panelStyle.Width(leftWidth).Render(m.renderSummaryPanel())
	content := panelStyle.Width(rightWidth).Render(m.renderContentPanel())

	var body string
	if m.width > 100 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, summary, "   ", content)
	} else {
		body = lipgloss.JoinVertical(lipgloss.Left, summary, "", content)
	}

	footer := mutedStyle.Render("Controls: Up/Down navigate, Enter select/submit, Tab next field, q quit")
	return frameStyle.Render(body + "\n\n" + footer)
}

func (m *snapVaultTUI) renderSummaryPanel() string {
	lines := []string{headerStyle.Render("SnapVault Setup"), ""}
	lines = append(lines, "Selected NAS connections:")

	selected := m.selectedSharesForRuntime()
	if len(selected) == 0 {
		lines = append(lines, "  - none")
	} else {
		for _, share := range selected {
			lines = append(lines, "  - "+formatShareForDisplay(share))
		}
	}

	lines = append(lines, "")
	lines = append(lines, "SD mount path:")
	if strings.TrimSpace(m.resultMount) == "" {
		lines = append(lines, "  - not selected")
	} else {
		lines = append(lines, "  - "+m.resultMount)
	}

	lines = append(lines, "")
	lines = append(lines, "Photoshoot:")
	if strings.TrimSpace(m.resultName) == "" {
		lines = append(lines, "  - not set")
	} else {
		lines = append(lines, "  - "+m.resultName)
	}

	if m.folderName != "" {
		lines = append(lines, "", "Target folder:", "  - "+m.folderName)
	}

	lines = append(lines, "", fmt.Sprintf("Workers: %d", m.workers), fmt.Sprintf("SMB timeout: %s", m.timeout))

	if m.step == stepTransfer || m.step == stepSummary || m.step == stepDone {
		duration := time.Since(m.transferStartedAt)
		if !m.transferEndedAt.IsZero() {
			duration = m.transferEndedAt.Sub(m.transferStartedAt)
		}
		lines = append(lines, "", "Transfer status:")
		lines = append(lines, fmt.Sprintf("  - %d / %d photos", m.transferCompleted, m.transferTotal))
		lines = append(lines, fmt.Sprintf("  - Duration: %s", duration.Round(time.Second)))
	}

	return strings.Join(lines, "\n")
}

func (m *snapVaultTUI) renderContentPanel() string {
	var lines []string
	lines = append(lines, headerStyle.Render("Step"))
	lines = append(lines, "")
	if m.statusMessage != "" {
		lines = append(lines, m.statusMessage, "")
	}

	switch m.step {
	case stepSelectShares:
		lines = append(lines,
			"1) Choose NAS connections",
			"Space toggles a saved connection. Press Enter on Continue when done.",
			"",
		)
		for i, share := range m.availableShares {
			cursor := " "
			if i == m.shareCursor {
				cursor = ">"
			}
			checked := " "
			if m.selectedShares[smbShareKey(share)] {
				checked = "x"
			}
			lines = append(lines, fmt.Sprintf("%s [%s] %s", cursor, checked, formatShareForDisplay(share)))
		}
		addIdx := len(m.availableShares)
		continueIdx := len(m.availableShares) + 1

		addCursor := " "
		if m.shareCursor == addIdx {
			addCursor = ">"
		}
		lines = append(lines, fmt.Sprintf("%s Add new NAS connection", addCursor))

		contCursor := " "
		if m.shareCursor == continueIdx {
			contCursor = ">"
		}
		lines = append(lines, fmt.Sprintf("%s Continue", contCursor))

	case stepAddShare:
		lines = append(lines,
			"1A) Add NAS connection",
			"Use host + share path (starting from share name).",
			"Example host: 192.168.1.33",
			"Example share path: general/snapvault",
			"",
		)
		if m.isValidating {
			lines = append(lines, mutedStyle.Render("Testing connection..."))
		}
		for i := range m.addShareInputs {
			lines = append(lines, m.addShareInputs[i].View())
		}

	case stepSelectMount:
		lines = append(lines,
			"2) Choose SD card mount",
			"",
		)
		for i, candidate := range m.mountCandidates {
			cursor := " "
			if i == m.mountCursor {
				cursor = ">"
			}
			lines = append(lines, fmt.Sprintf("%s %s", cursor, formatMountCandidate(candidate)))
		}
		manualIdx := len(m.mountCandidates)
		cursor := " "
		if m.mountCursor == manualIdx {
			cursor = ">"
		}
		lines = append(lines, fmt.Sprintf("%s Enter manual mount path", cursor))

	case stepManualMount:
		lines = append(lines,
			"2A) Enter manual mount path",
			"Examples: /media/kiran/SDCARD, /run/media/kiran/CAMERA_CARD",
			"",
			m.manualMountInput.View(),
		)

	case stepPhotoshoot:
		lines = append(lines,
			"3) Enter photoshoot name",
			"",
			m.photoshootInput.View(),
		)

	case stepTransfer:
		percent := progressPercent(m.transferCompleted, m.transferTotal)
		lines = append(lines,
			"4) Transfer in progress",
			"",
			m.progressBar.ViewAs(percent),
			fmt.Sprintf("%d / %d photos processed", m.transferCompleted, m.transferTotal),
		)
		if m.transferFile != "" {
			lines = append(lines, fmt.Sprintf("Current file: %s", m.transferFile))
		}

	case stepSummary:
		successFiles := m.transferCompleted
		if m.transferTotal > 0 {
			successFiles = m.transferTotal
		}
		if len(m.transferErrors) == 0 && m.transferErr == nil {
			lines = append(lines, okStyle.Render("Transfer complete"))
		} else {
			lines = append(lines, errStyle.Render("Transfer completed with issues"))
		}
		lines = append(lines, "", fmt.Sprintf("Photos discovered: %d", m.transferTotal), fmt.Sprintf("Photos processed: %d", m.transferCompleted), fmt.Sprintf("Share transfer errors: %d", len(m.transferErrors)), fmt.Sprintf("Estimated successful files: %d", max(successFiles-uniqueFailedFiles(m.transferErrors), 0)))
		if m.transferErr != nil {
			lines = append(lines, "", "Fatal error:", "  "+m.transferErr.Error())
		}
		if len(m.transferErrors) > 0 {
			lines = append(lines, "", "Sample errors:")
			for i, te := range m.transferErrors {
				if i >= 4 {
					lines = append(lines, fmt.Sprintf("  ... and %d more", len(m.transferErrors)-4))
					break
				}
				lines = append(lines, fmt.Sprintf("  - %s (%s): %v", filepath.Base(te.FilePath), te.Share, te.Error))
			}
		}
		lines = append(lines, "", mutedStyle.Render("Press Enter to exit"))
	}

	return strings.Join(lines, "\n")
}

func (m *snapVaultTUI) updateSelectShares(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	totalItems := len(m.availableShares) + 2
	switch keyMsg.String() {
	case "up", "k":
		if m.shareCursor > 0 {
			m.shareCursor--
		}
	case "down", "j":
		if m.shareCursor < totalItems-1 {
			m.shareCursor++
		}
	case " ":
		if m.shareCursor < len(m.availableShares) {
			share := m.availableShares[m.shareCursor]
			key := smbShareKey(share)
			if m.selectedShares[key] {
				delete(m.selectedShares, key)
			} else {
				m.selectedShares[key] = true
			}
		}
	case "enter":
		switch {
		case m.shareCursor < len(m.availableShares):
			share := m.availableShares[m.shareCursor]
			key := smbShareKey(share)
			if m.selectedShares[key] {
				delete(m.selectedShares, key)
			} else {
				m.selectedShares[key] = true
			}
		case m.shareCursor == len(m.availableShares):
			m.step = stepAddShare
			m.statusMessage = ""
			m.focusAddShareInput(0)
		default:
			if len(m.selectedSharesForRuntime()) == 0 {
				m.statusMessage = "Select at least one NAS connection before continuing."
				return m, nil
			}
			return m.advanceAfterShares()
		}
	}

	return m, nil
}

func (m *snapVaultTUI) updateAddShare(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.isValidating {
		return m, nil
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if ok {
		switch keyMsg.String() {
		case "esc":
			m.step = stepSelectShares
			return m, nil
		case "tab", "shift+tab", "up", "down":
			if keyMsg.String() == "up" || keyMsg.String() == "shift+tab" {
				m.addShareFocus--
			} else {
				m.addShareFocus++
			}
			if m.addShareFocus > len(m.addShareInputs)-1 {
				m.addShareFocus = 0
			}
			if m.addShareFocus < 0 {
				m.addShareFocus = len(m.addShareInputs) - 1
			}
			m.focusAddShareInput(m.addShareFocus)
			return m, nil
		case "enter":
			if m.addShareFocus < len(m.addShareInputs)-1 {
				m.addShareFocus++
				m.focusAddShareInput(m.addShareFocus)
				return m, nil
			}

			hostOrTarget := strings.TrimSpace(m.addShareInputs[0].Value())
			sharePath := strings.TrimSpace(m.addShareInputs[1].Value())
			username := strings.TrimSpace(m.addShareInputs[2].Value())
			password := m.addShareInputs[3].Value()

			if hostOrTarget == "" || username == "" {
				m.statusMessage = "NAS URL/IP and username are required."
				return m, nil
			}

			var (
				host     string
				port     int
				share    string
				basePath string
				err      error
			)

			if strings.Contains(hostOrTarget, "/") && sharePath == "" {
				host, port, share, basePath, err = parseManualSMBTarget(hostOrTarget)
				if err != nil {
					m.statusMessage = fmt.Sprintf("Invalid combined target: %v", err)
					return m, nil
				}
			} else {
				if sharePath == "" {
					m.statusMessage = "Share path is required (example: general/snapvault)."
					return m, nil
				}

				host, port, err = parseSMBHostPort(hostOrTarget)
				if err != nil {
					m.statusMessage = fmt.Sprintf("Invalid NAS URL/IP: %v", err)
					return m, nil
				}

				share, basePath, err = parseSMBSharePath(sharePath)
				if err != nil {
					m.statusMessage = fmt.Sprintf("Invalid share path: %v", err)
					return m, nil
				}
			}

			shareConfig := SMBConfig{
				Host:     host,
				Port:     port,
				Share:    share,
				BasePath: basePath,
				Username: username,
				Password: password,
			}

			m.isValidating = true
			m.statusMessage = fmt.Sprintf("Testing connection to %s ...", formatShareForDisplay(shareConfig))
			return m, testSMBConnectionCmd(shareConfig, 15*time.Second)
		}
	}

	cmds := make([]tea.Cmd, len(m.addShareInputs))
	for i := range m.addShareInputs {
		m.addShareInputs[i], cmds[i] = m.addShareInputs[i].Update(msg)
	}
	return m, tea.Batch(cmds...)
}

func (m *snapVaultTUI) updateSelectMount(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	totalItems := len(m.mountCandidates) + 1
	switch keyMsg.String() {
	case "up", "k":
		if m.mountCursor > 0 {
			m.mountCursor--
		}
	case "down", "j":
		if m.mountCursor < totalItems-1 {
			m.mountCursor++
		}
	case "enter":
		if m.mountCursor == len(m.mountCandidates) {
			m.step = stepManualMount
			m.statusMessage = ""
			m.manualMountInput.Focus()
			return m, nil
		}

		selected := m.mountCandidates[m.mountCursor].Path
		if err := validateMountPath(selected); err != nil {
			m.statusMessage = fmt.Sprintf("Selected mount path is invalid: %v", err)
			return m, nil
		}

		m.resultMount = selected
		return m.advanceAfterMount()
	}

	return m, nil
}

func (m *snapVaultTUI) updateManualMount(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if ok {
		switch keyMsg.String() {
		case "esc":
			m.manualMountInput.Blur()
			m.step = stepSelectMount
			return m, nil
		case "enter":
			mountPath := strings.TrimSpace(m.manualMountInput.Value())
			if mountPath == "" {
				m.statusMessage = "Mount path is required."
				return m, nil
			}
			if err := validateMountPath(mountPath); err != nil {
				m.statusMessage = fmt.Sprintf("Mount path is invalid: %v", err)
				return m, nil
			}
			m.resultMount = mountPath
			return m.advanceAfterMount()
		}
	}

	var cmd tea.Cmd
	m.manualMountInput, cmd = m.manualMountInput.Update(msg)
	return m, cmd
}

func (m *snapVaultTUI) updatePhotoshoot(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if ok && keyMsg.String() == "enter" {
		name := strings.TrimSpace(m.photoshootInput.Value())
		if name == "" {
			m.statusMessage = "Photoshoot name is required."
			return m, nil
		}

		m.resultName = name
		m.statusMessage = ""
		m.step = stepTransfer
		return m.startTransfer()
	}

	var cmd tea.Cmd
	m.photoshootInput, cmd = m.photoshootInput.Update(msg)
	return m, cmd
}

func (m *snapVaultTUI) advanceAfterShares() (tea.Model, tea.Cmd) {
	if m.mountDefault != "" {
		if err := validateMountPath(m.mountDefault); err == nil {
			m.resultMount = m.mountDefault
			return m.advanceAfterMount()
		}
		m.statusMessage = fmt.Sprintf("Provided -mount is invalid (%s). Select a mount path in TUI.", m.mountDefault)
	}

	if len(m.mountCandidates) == 0 {
		m.step = stepManualMount
		m.manualMountInput.Focus()
		return m, nil
	}

	m.step = stepSelectMount
	return m, nil
}

func (m *snapVaultTUI) advanceAfterMount() (tea.Model, tea.Cmd) {
	m.manualMountInput.Blur()

	if m.nameDefault != "" {
		m.resultName = m.nameDefault
		m.step = stepTransfer
		return m.startTransfer()
	}

	m.step = stepPhotoshoot
	m.photoshootInput.Focus()
	return m, nil
}

func (m *snapVaultTUI) startTransfer() (tea.Model, tea.Cmd) {
	selected := m.selectedSharesForRuntime()
	if len(selected) == 0 {
		m.step = stepSelectShares
		m.statusMessage = "Select at least one NAS connection before starting transfer."
		return m, nil
	}

	if strings.TrimSpace(m.resultMount) == "" {
		m.step = stepSelectMount
		m.statusMessage = "Select a mount path before starting transfer."
		return m, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.transferCancel = cancel
	m.transferStartedAt = time.Now()
	m.transferEndedAt = time.Time{}
	m.transferTotal = 0
	m.transferCompleted = 0
	m.transferFile = ""
	m.transferErr = nil
	m.transferErrors = nil
	m.transferEvents = make(chan tea.Msg, 256)

	go runTransferWorkflow(ctx, m.transferEvents, m.resultMount, m.resultName, selected, m.timeout, m.workers)
	return m, waitForTransferMsg(m.transferEvents)
}

func runTransferWorkflow(
	ctx context.Context,
	events chan<- tea.Msg,
	mountPoint, photoshootName string,
	shares []SMBConfig,
	timeout time.Duration,
	workers int,
) {
	defer close(events)

	folderName := fmt.Sprintf("%d - %s", time.Now().Year(), photoshootName)
	events <- transferStartedMsg{folderName: folderName}

	config := &Config{SMBShares: shares}
	connections, err := establishConnections(ctx, config, timeout)
	if err != nil {
		events <- transferFinishedMsg{err: fmt.Errorf("establishing SMB connections: %w", err)}
		return
	}
	defer closeConnections(connections)

	hook := &TransferProgressHook{
		OnStart: func(total int) {
			events <- transferStartedMsg{total: total}
		},
		OnProgress: func(total, completed int, filePath string) {
			events <- transferProgressMsg{total: total, completed: completed, filePath: filePath}
		},
	}

	transferErrors, err := processPhotos(ctx, mountPoint, folderName, connections, workers, hook)
	events <- transferFinishedMsg{err: err, errors: transferErrors}
}

func waitForTransferMsg(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return transferChannelClosedMsg{}
		}
		return msg
	}
}

func (m *snapVaultTUI) syncInputWidths() {
	formWidth := max(20, m.width/2-12)
	for i := range m.addShareInputs {
		m.addShareInputs[i].Width = formWidth
	}
	m.manualMountInput.Width = formWidth
	m.photoshootInput.Width = formWidth
	m.progressBar.Width = max(24, m.width/2-16)
}

func (m *snapVaultTUI) focusAddShareInput(index int) {
	for i := range m.addShareInputs {
		if i == index {
			m.addShareInputs[i].Focus()
		} else {
			m.addShareInputs[i].Blur()
		}
	}
}

func (m *snapVaultTUI) resetAddShareForm() {
	for i := range m.addShareInputs {
		m.addShareInputs[i].SetValue("")
	}
	m.addShareFocus = 0
	m.focusAddShareInput(0)
}

func (m *snapVaultTUI) selectedSharesForRuntime() []SMBConfig {
	selected := make([]SMBConfig, 0, len(m.selectedShares))
	for _, share := range m.availableShares {
		if !m.selectedShares[smbShareKey(share)] {
			continue
		}
		resolved := share
		resolved.Password = os.ExpandEnv(resolved.Password)
		selected = append(selected, resolved)
	}
	return selected
}

func upsertShareConfig(shares []SMBConfig, newShare SMBConfig) []SMBConfig {
	key := smbShareKey(newShare)
	for i, share := range shares {
		if smbShareKey(share) == key {
			shares[i] = newShare
			return shares
		}
	}
	return append(shares, newShare)
}

func smbShareKey(share SMBConfig) string {
	port := share.Port
	if port == 0 {
		port = 445
	}
	return strings.ToLower(fmt.Sprintf("%s|%d|%s|%s|%s", share.Host, port, share.Share, share.BasePath, share.Username))
}

func formatShareForDisplay(share SMBConfig) string {
	port := share.Port
	if port == 0 {
		port = 445
	}
	target := fmt.Sprintf("%s:%d/%s", share.Host, port, share.Share)
	if share.BasePath != "" {
		target = target + "/" + strings.TrimPrefix(filepathToSlash(share.BasePath), "/")
	}
	return fmt.Sprintf("%s (user=%s)", target, share.Username)
}

func formatMountCandidate(candidate MountCandidate) string {
	if candidate.Source != "" && candidate.FSType != "" {
		return fmt.Sprintf("%s [device=%s, fs=%s]", candidate.Path, candidate.Source, candidate.FSType)
	}
	return candidate.Path
}

func filepathToSlash(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

func testSMBConnectionCmd(share SMBConfig, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		testConfig := share
		testConfig.Password = os.ExpandEnv(testConfig.Password)
		err := validateSMBConnection(testConfig, timeout)
		return smbValidationResultMsg{share: share, err: err}
	}
}

func validateSMBConnection(share SMBConfig, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	session, err := connectSMB(ctx, share, timeout)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer session.Logoff()

	mountedShare, err := session.Mount(share.Share)
	if err != nil {
		return fmt.Errorf("mount %s: %w", share.Share, err)
	}
	defer mountedShare.Umount()

	return nil
}

func progressPercent(completed, total int) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(completed) / float64(total)
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

func uniqueFailedFiles(errs []TransferError) int {
	seen := make(map[string]struct{})
	for _, err := range errs {
		seen[err.FilePath] = struct{}{}
	}
	return len(seen)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
