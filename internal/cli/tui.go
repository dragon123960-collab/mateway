package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tool"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

const maxTUIEventLines = 2500

type TUIOptions struct {
	Config     *config.Root
	SessionKey string
	In         io.Reader
	Out        io.Writer
}

func RunTUI(ctx context.Context, opts TUIOptions) error {
	if opts.Config == nil {
		return fmt.Errorf("config is required")
	}
	inFile, ok := opts.In.(*os.File)
	if opts.In == nil {
		inFile = os.Stdin
		ok = true
	}
	outFile, outOK := opts.Out.(*os.File)
	if opts.Out == nil {
		outFile = os.Stdout
		outOK = true
	}
	if !ok || !outOK || !CanRunTUI(inFile, outFile) {
		return fmt.Errorf("tui requires an interactive terminal")
	}
	model := newTUIModel(ctx, opts.Config, ResolveSessionKey(opts.SessionKey))
	program := tea.NewProgram(
		model,
		tea.WithInput(inFile),
		tea.WithOutput(outFile),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	model.program = program
	_, err := program.Run()
	return err
}

func CanRunTUI(in, out *os.File) bool {
	return in != nil && out != nil && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

type tuiModel struct {
	ctx        context.Context
	cfg        *config.Root
	sessionKey string
	agent      config.AgentProfileConfig
	model      tuiModelInfo
	program    *tea.Program

	viewport      viewport.Model
	input         textarea.Model
	events        []string
	width         int
	height        int
	sidebar       int
	sidebarScroll int

	running       bool
	status        string
	progress      int
	toolEvents    int
	lastTool      string
	lastToolState string
	currentTask   string
	liveSteps     []session.TaskStep
	errText       string

	commandPanel bool
	commandIndex int
	picker       *tuiPicker
	history      []string
	historyIndex int
	autoFollow   bool
	newEvents    int
}

type tuiPicker struct {
	Kind   string
	Title  string
	Search string
	Index  int
	Items  []tuiPickerItem
}

type tuiPickerItem struct {
	Label    string
	Detail   string
	Value    string
	Status   string
	Shortcut string
}

type tuiProgressMsg struct {
	line      string
	status    string
	tool      string
	toolState string
	summary   string
	duration  int64
}

type tuiResultMsg struct {
	reply     channel.OutboundMessage
	followUps []channel.OutboundMessage
	tracePath string
	err       error
}

func newTUIModel(ctx context.Context, cfg *config.Root, sessionKey string) *tuiModel {
	agent := cfg.DefaultAgent()
	input := textarea.New()
	input.Placeholder = `Ask anything...`
	input.Prompt = ""
	input.SetPromptFunc(0, func(int) string { return "" })
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = 0
	input.MaxHeight = 4
	input.SetHeight(3)
	input.CharLimit = 0
	input.FocusedStyle.Base = lipgloss.NewStyle()
	input.BlurredStyle.Base = lipgloss.NewStyle()
	input.Focus()
	vp := viewport.New(80, 20)
	model := &tuiModel{
		ctx:          ctx,
		cfg:          cfg,
		sessionKey:   sessionKey,
		agent:        agent,
		model:        currentTUIModel(cfg, agent),
		viewport:     vp,
		input:        input,
		status:       "Idle",
		historyIndex: -1,
		autoFollow:   true,
	}
	model.addSessionBanner()
	return model
}

func (m *tuiModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
	case tea.KeyMsg:
		if m.picker != nil {
			return m.updatePicker(msg)
		}
		if m.commandPanel {
			return m.updateCommandPanel(msg)
		}
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.commandPanel = false
			return m, nil
		case "enter":
			return m, m.submit()
		case "/":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.commandPanel = true
				m.commandIndex = 0
				m.input.SetValue("/")
				return m, nil
			}
		case "up":
			if (strings.TrimSpace(m.input.Value()) == "" || m.historyIndex >= 0) && len(m.history) > 0 {
				m.historyUp()
				return m, nil
			}
			m.viewport.LineUp(1)
			m.autoFollow = false
			return m, nil
		case "down":
			if (strings.TrimSpace(m.input.Value()) == "" || m.historyIndex >= 0) && len(m.history) > 0 {
				m.historyDown()
				return m, nil
			}
			m.viewport.LineDown(1)
			if m.viewport.AtBottom() {
				m.autoFollow = true
				m.newEvents = 0
			}
			return m, nil
		case "pgup":
			m.viewport.ViewUp()
			m.autoFollow = false
			return m, nil
		case "pgdown":
			m.viewport.ViewDown()
			if m.viewport.AtBottom() {
				m.autoFollow = true
				m.newEvents = 0
			}
			return m, nil
		case "end":
			m.viewport.GotoBottom()
			m.autoFollow = true
			m.newEvents = 0
			return m, nil
		}
	case tea.MouseMsg:
		sidebarWidth := m.sidebarWidth()
		if sidebarWidth > 0 && msg.X >= m.contentWidth() {
			switch msg.Type {
			case tea.MouseWheelUp:
				m.sidebarScroll = maxInt(0, m.sidebarScroll-3)
			case tea.MouseWheelDown:
				m.sidebarScroll += 3
			}
			return m, nil
		}
		switch msg.Type {
		case tea.MouseWheelUp:
			m.autoFollow = false
		case tea.MouseWheelDown:
			if m.viewport.AtBottom() {
				m.autoFollow = true
				m.newEvents = 0
			}
		}
	case tuiProgressMsg:
		m.status = msg.status
		m.progress++
		if strings.TrimSpace(msg.tool) != "" {
			m.toolEvents++
			m.lastTool = msg.tool
			m.lastToolState = msg.toolState
			m.recordLiveStep(msg)
		}
		if strings.TrimSpace(msg.line) != "" {
			m.addEvent(msg.line)
		}
		return m, nil
	case tuiResultMsg:
		m.running = false
		m.status = "Idle"
		m.currentTask = ""
		m.liveSteps = nil
		if msg.err != nil {
			m.errText = msg.err.Error()
			m.addEvent("")
			m.addEvent(colorize("Error", ansiRed, true))
			m.addEvent(compactBlock(msg.err.Error(), 600))
			return m, nil
		}
		m.addEvent("")
		m.addEvent(colorize("Assistant", ansiGreen, true))
		for _, out := range (channel.OutboundBatch{Reply: msg.reply, FollowUps: msg.followUps}).Messages() {
			rendered := renderMarkdownForTUI(out.Text, maxInt(40, m.contentWidth()-4))
			for _, line := range wrapLines(rendered, maxInt(40, m.contentWidth()-4)) {
				m.addEvent(line)
			}
			m.addEvent("")
		}
		if strings.TrimSpace(msg.tracePath) != "" {
			m.addEvent(colorize("trace: "+msg.tracePath, ansiDim, true))
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	var inputCmd tea.Cmd
	m.input, inputCmd = m.input.Update(msg)
	cmd = tea.Batch(cmd, inputCmd)
	if m.commandPanel && !strings.HasPrefix(strings.TrimSpace(m.input.Value()), "/") {
		m.commandPanel = false
	}
	return m, cmd
}

func (m *tuiModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	footer := m.footerView()
	available := maxInt(1, m.height-lipgloss.Height(footer))
	contentWidth := m.contentWidth()
	m.viewport.Width = contentWidth
	m.viewport.Height = available
	content := lipgloss.NewStyle().
		Width(contentWidth).
		Height(available).
		Render(m.viewport.View())
	main := fitRenderedHeight(lipgloss.JoinVertical(lipgloss.Left, content, footer), m.height)
	if m.sidebarWidth() == 0 {
		if m.picker != nil {
			return centerModal(m.width, m.height, m.pickerView(minInt(m.width-8, 88)))
		}
		if m.commandPanel {
			return centerModal(m.width, m.height, m.commandPanelView(minInt(m.width-8, 84)))
		}
		return main
	}
	sidebar := fitRenderedHeight(m.sidebarView(m.height), m.height)
	view := lipgloss.JoinHorizontal(lipgloss.Top, main, sidebar)
	if m.picker != nil {
		return centerModal(m.width, m.height, m.pickerView(minInt(m.width-8, 88)))
	}
	if m.commandPanel {
		return centerModal(m.width, m.height, m.commandPanelView(minInt(m.width-8, 84)))
	}
	return view
}

func fitRenderedHeight(view string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m *tuiModel) resize() {
	footer := m.footerView()
	m.sidebar = m.sidebarWidth()
	m.viewport.Width = m.contentWidth()
	m.viewport.Height = maxInt(1, m.height-lipgloss.Height(footer))
	m.input.SetWidth(maxInt(8, m.contentWidth()-4))
	m.input.SetHeight(m.inputHeight())
	m.refreshViewport()
}

func (m *tuiModel) footerView() string {
	width := m.contentWidth()
	var parts []string
	m.input.SetHeight(m.inputHeight())
	input := tuiInputStyle(width).Render(m.input.View())
	status := m.statusLine(width)
	parts = append(parts, input, status)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m *tuiModel) statusLine(width int) string {
	mode := colorize(firstNonEmpty(m.status, "Idle"), ansiBlue, true)
	parts := []string{mode, colorize(m.model.Display(), ansiDim, true)}
	if m.running {
		parts = append(parts, colorize(m.progressSummary(), ansiDim, true))
	}
	if m.newEvents > 0 {
		parts = append(parts, colorize(fmt.Sprintf("%d new events", m.newEvents), ansiYellow, true))
	}
	right := statusRight(width, []string{
		"agent:" + firstNonEmpty(m.agent.ID, "main"),
		"session:" + strings.TrimPrefix(m.sessionKey, "cli:"),
		"ctrl+c exit",
		"/ commands",
	})
	left := strings.Join(parts, " · ")
	gap := width - visibleLen(left) - visibleLen(right)
	if gap < 1 {
		return truncateANSI(left+" · "+right, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *tuiModel) inputHeight() int {
	value := m.input.Value()
	lines := 3
	if strings.TrimSpace(value) != "" {
		lines = maxInt(3, strings.Count(value, "\n")+1)
	}
	if lines > 4 {
		return 4
	}
	return lines
}

func (m *tuiModel) progressSummary() string {
	if m.toolEvents > 0 {
		return fmt.Sprintf("%d tool updates", m.toolEvents)
	}
	if m.progress > 0 {
		return "working"
	}
	return "waiting"
}

func (m *tuiModel) commandPanelView(width int) string {
	items := m.filteredCommandItems()
	if len(items) == 0 {
		items = []commandPanelItem{{
			Section: "Search",
			Label:   "No matching commands",
			What:    "try another search",
		}}
	}
	if m.commandIndex >= len(items) {
		m.commandIndex = len(items) - 1
	}
	if m.commandIndex < 0 {
		m.commandIndex = 0
	}
	lines := []string{
		paletteHeaderLine(width),
		paletteSearchLine(m.commandSearch(), width),
		"",
	}
	lastSection := ""
	shown := 0
	for i, item := range visiblePaletteItems(items, m.commandIndex, 14) {
		absolute := i
		if len(items) > 14 {
			absolute = paletteVisibleStart(len(items), m.commandIndex, 14) + i
		}
		if item.Section != lastSection {
			if lastSection != "" {
				lines = append(lines, "")
			}
			lines = append(lines, colorize(item.Section, ansiBlue, true))
			lastSection = item.Section
		}
		selected := absolute == m.commandIndex
		lines = append(lines, paletteItemLine(item, selected, width))
		shown++
	}
	if shown == 0 {
		lines = append(lines, colorize("No matching commands", ansiDim, true))
	}
	lines = append(lines, "", colorize("Esc close · Enter run/fill · ↑/↓ select", ansiDim, true))
	return paletteStyle(width).Render(strings.Join(lines, "\n"))
}

func paletteHeaderLine(width int) string {
	left := colorize("Commands", ansiGreen, true)
	right := colorize("esc", ansiDim, true)
	gap := maxInt(1, width-6-visibleLen(left)-visibleLen(right))
	return left + strings.Repeat(" ", gap) + right
}

func paletteSearchLine(search string, width int) string {
	if strings.TrimSpace(search) == "" {
		search = "Search"
	}
	return truncateANSI(colorize("Search ", ansiDim, true)+search, maxInt(10, width-6))
}

func paletteItemLine(item commandPanelItem, selected bool, width int) string {
	label := firstNonEmpty(item.Label, item.Command)
	shortcut := colorize(item.Shortcut, ansiDim, true)
	detail := colorize(item.What, ansiDim, true)
	left := label
	if strings.TrimSpace(detail) != "" && !selected {
		left += " " + detail
	}
	if strings.TrimSpace(shortcut) == "" {
		if selected {
			return selectedPaletteLine(truncateANSI(left, width-6), width)
		}
		return truncateANSI("  "+left, width-6)
	}
	gap := maxInt(1, width-8-visibleLen(left)-visibleLen(shortcut))
	line := left + strings.Repeat(" ", gap) + shortcut
	if selected {
		return selectedPaletteLine(truncateANSI(line, width-6), width)
	}
	return truncateANSI("  "+line, width-6)
}

func selectedPaletteLine(text string, width int) string {
	innerWidth := maxInt(8, width-6)
	padded := padRightVisible("  "+text, innerWidth)
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("255")).
		Background(lipgloss.Color("238")).
		Bold(true).
		Render(padded)
}

func visiblePaletteItems(items []commandPanelItem, selected, limit int) []commandPanelItem {
	if len(items) <= limit {
		return items
	}
	start := paletteVisibleStart(len(items), selected, limit)
	return items[start:minInt(len(items), start+limit)]
}

func paletteVisibleStart(total, selected, limit int) int {
	if total <= limit {
		return 0
	}
	start := selected - limit/2
	if start < 0 {
		return 0
	}
	if start+limit > total {
		return total - limit
	}
	return start
}

func paletteStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("238")).
		Background(lipgloss.Color("235")).
		Padding(1, 2).
		Width(maxInt(40, width))
}

func (m *tuiModel) pickerView(width int) string {
	if m.picker == nil {
		return ""
	}
	items := m.filteredPickerItems()
	if len(items) == 0 {
		items = []tuiPickerItem{{Label: "No matches", Detail: "try another search"}}
	}
	if m.picker.Index >= len(items) {
		m.picker.Index = len(items) - 1
	}
	if m.picker.Index < 0 {
		m.picker.Index = 0
	}
	lines := []string{
		pickerHeaderLine(m.picker.Title, width),
		paletteSearchLine(m.picker.Search, width),
		"",
	}
	for i, item := range visiblePickerItems(items, m.picker.Index, 12) {
		absolute := i
		if len(items) > 12 {
			absolute = paletteVisibleStart(len(items), m.picker.Index, 12) + i
		}
		lines = append(lines, pickerItemLine(item, absolute == m.picker.Index, width))
	}
	lines = append(lines, "", colorize("Esc back · Enter select · ↑/↓ move · type to filter", ansiDim, true))
	return paletteStyle(width).Render(strings.Join(lines, "\n"))
}

func pickerHeaderLine(title string, width int) string {
	left := colorize(firstNonEmpty(title, "Select"), ansiGreen, true)
	right := colorize("esc", ansiDim, true)
	gap := maxInt(1, width-6-visibleLen(left)-visibleLen(right))
	return left + strings.Repeat(" ", gap) + right
}

func pickerItemLine(item tuiPickerItem, selected bool, width int) string {
	label := item.Label
	status := colorize(item.Status, ansiDim, true)
	detail := colorize(item.Detail, ansiDim, true)
	left := label
	if strings.TrimSpace(detail) != "" && !selected {
		left += " " + detail
	}
	if strings.TrimSpace(status) != "" {
		gap := maxInt(1, width-8-visibleLen(left)-visibleLen(status))
		left = left + strings.Repeat(" ", gap) + status
	}
	if selected {
		return selectedPaletteLine(truncateANSI(left, width-6), width)
	}
	return truncateANSI("  "+left, width-6)
}

func visiblePickerItems(items []tuiPickerItem, selected, limit int) []tuiPickerItem {
	if len(items) <= limit {
		return items
	}
	start := paletteVisibleStart(len(items), selected, limit)
	return items[start:minInt(len(items), start+limit)]
}

func (m *tuiModel) filteredPickerItems() []tuiPickerItem {
	if m.picker == nil {
		return nil
	}
	filter := strings.ToLower(strings.TrimSpace(m.picker.Search))
	if filter == "" {
		return m.picker.Items
	}
	var out []tuiPickerItem
	for _, item := range m.picker.Items {
		haystack := strings.ToLower(strings.Join([]string{item.Label, item.Detail, item.Value, item.Status}, " "))
		if strings.Contains(haystack, filter) {
			out = append(out, item)
		}
	}
	return out
}

func (m *tuiModel) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.picker = nil
		return m, nil
	case "backspace":
		if m.picker.Search != "" {
			rs := []rune(m.picker.Search)
			m.picker.Search = string(rs[:len(rs)-1])
			m.picker.Index = 0
		}
		return m, nil
	case "up":
		m.picker.Index--
		if m.picker.Index < 0 {
			m.picker.Index = len(m.filteredPickerItems()) - 1
		}
		return m, nil
	case "down":
		m.picker.Index++
		items := m.filteredPickerItems()
		if len(items) == 0 || m.picker.Index >= len(items) {
			m.picker.Index = 0
		}
		return m, nil
	case "enter":
		return m, m.acceptPicker()
	}
	if len(msg.Runes) > 0 {
		m.picker.Search += string(msg.Runes)
		m.picker.Index = 0
	}
	return m, nil
}

func (m *tuiModel) acceptPicker() tea.Cmd {
	if m.picker == nil {
		return nil
	}
	items := m.filteredPickerItems()
	if len(items) == 0 {
		return nil
	}
	if m.picker.Index < 0 || m.picker.Index >= len(items) {
		m.picker.Index = 0
	}
	item := items[m.picker.Index]
	kind := m.picker.Kind
	m.picker = nil
	switch kind {
	case "sessions":
		m.sessionKey = item.Value
		m.addEvent("session: " + item.Value)
	case "models":
		m.switchTUIModel(item.Value)
	case "tools":
		m.toggleTool(item.Value, item.Status)
	}
	return nil
}

func (m *tuiModel) switchTUIModel(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	m.cfg.Model.Default = name
	m.agent.Model.Default = name
	m.model = currentTUIModel(m.cfg, m.agent)
	m.addEvent("model: " + m.model.Display())
}

func (m *tuiModel) toggleTool(name, status string) {
	var (
		change ToolAccessChange
		err    error
	)
	if strings.EqualFold(status, "disabled") {
		change, err = EnableTool(m.cfg, m.agent.ID, name)
	} else {
		change, err = DisableTool(m.cfg, m.agent.ID, name)
	}
	if err != nil {
		m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
		return
	}
	var out bytes.Buffer
	PrintToolAccessChange(&out, change)
	m.addOutput(out.String())
	m.agent = m.cfg.DefaultAgent()
}

func centerModal(width, height int, panel string) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, panel)
}

func padRightVisible(text string, width int) string {
	n := visibleLen(text)
	if n >= width {
		return text
	}
	return text + strings.Repeat(" ", width-n)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func panelStyle(width int, color string) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(color)).
		Padding(0, 1).
		Width(maxInt(8, width-2))
}

func tuiInputStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Padding(0, 1).
		Width(maxInt(8, width-2))
}

func (m *tuiModel) contentWidth() int {
	if m.sidebar > 0 {
		return maxInt(40, m.width-m.sidebar)
	}
	return m.width
}

func (m *tuiModel) sidebarWidth() int {
	if m.width < 120 {
		return 0
	}
	width := m.width / 4
	if width < 28 {
		return 28
	}
	if width > 38 {
		return 38
	}
	return width
}

func (m *tuiModel) sidebarView(height int) string {
	width := m.sidebarWidth()
	if width == 0 {
		return ""
	}
	state, _ := session.NewStore(m.cfg.App.Home).Load(m.sessionKey)
	summary := m.sidebarSummary(state)
	lines := []string{colorize("Mateway", ansiGreen, true), ""}
	lines = appendSidebarSection(lines, "State",
		firstNonEmpty(m.status, "Idle"),
		fmt.Sprintf("%d process events", m.progress),
	)
	lines = appendSidebarSection(lines, "Session",
		summary.SessionName,
		fmt.Sprintf("%d messages", len(state.Messages)),
		fmt.Sprintf("%d tasks", len(state.Tasks)),
	)
	lines = appendSidebarSection(lines, "Agent",
		firstNonEmpty(m.agent.Name, m.agent.ID, "main"),
		m.model.Display(),
		"local cli",
	)
	lines = appendSidebarSection(lines, "Usage", summary.UsageLines...)
	lines = appendSidebarSection(lines, "Trace", summary.TraceLines...)
	lines = appendSidebarSection(lines, "Task", summary.TaskLines...)
	lines = appendSidebarSection(lines, "Tools", summary.ToolLines...)
	lines = m.fitSidebarLines(lines, maxInt(1, height-2), maxInt(8, width-5))
	return sidebarStyle(width, height).Render(strings.Join(lines, "\n"))
}

type tuiSidebarSummary struct {
	SessionName string
	UsageLines  []string
	TraceLines  []string
	TaskLines   []string
	ToolLines   []string
}

func (m *tuiModel) sidebarSummary(state session.State) tuiSidebarSummary {
	out := tuiSidebarSummary{SessionName: m.sessionDisplayName(state)}
	usage := state.Usage
	tracePath := latestTracePath(state)
	if tracePath != "" {
		if summary, err := runtime.SummarizeTrace(tracePath); err == nil {
			out.TraceLines = traceSummaryLines(summary)
			if usage.Requests == 0 && summary.ModelRequests > 0 {
				usage.Requests = summary.ModelRequests
				usage.InputTokens = summary.InputTokens
				usage.OutputTokens = summary.OutputTokens
				usage.TotalTokens = summary.TotalTokens
			}
		} else {
			out.TraceLines = []string{filepath.Base(tracePath), "unreadable"}
		}
	}
	if len(out.TraceLines) == 0 {
		out.TraceLines = []string{"none"}
	}
	out.UsageLines = usageLines(usage)
	if m.running {
		if task, ok := selectedTask(state); ok && task.Execution.Contract != nil {
			out.TaskLines = taskSidebarLines(task)
		} else {
			out.TaskLines = []string{"▾ Contract pending", "[•] " + compactInline(firstNonEmpty(m.currentTask, "waiting for runtime contract"), 84)}
		}
	} else {
		out.TaskLines = taskLines(state)
	}
	out.ToolLines = m.toolLines()
	return out
}

func (m *tuiModel) liveTaskLines() []string {
	lines := []string{"Status: " + firstNonEmpty(m.status, "running")}
	if strings.TrimSpace(m.currentTask) != "" {
		lines = append(lines, "Goal: "+compactInline(m.currentTask, 84))
	}
	if len(m.liveSteps) > 0 {
		lines = append(lines, recentStepLines(m.liveSteps, 4)...)
	} else {
		lines = append(lines, "Waiting for first tool/event")
	}
	return lines
}

func (m *tuiModel) sessionDisplayName(state session.State) string {
	key := firstNonEmpty(state.Key, m.sessionKey)
	if strings.HasPrefix(key, "cli:") {
		return strings.TrimPrefix(key, "cli:")
	}
	return key
}

func appendSidebarSection(lines []string, title string, values ...string) []string {
	var clean []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			clean = append(clean, value)
		}
	}
	if len(clean) == 0 {
		return lines
	}
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}
	lines = append(lines, sidebarSectionTitle(title))
	return append(lines, clean...)
}

func sidebarSectionTitle(title string) string {
	return colorize(strings.ToUpper(title), ansiBlue, true)
}

func usageLines(usage session.Usage) []string {
	if usage.Requests == 0 && usage.TotalTokens == 0 && usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.Cost == 0 {
		return []string{"none"}
	}
	lines := []string{
		fmt.Sprintf("%d requests", usage.Requests),
		fmt.Sprintf("%d tokens", usage.TotalTokens),
		fmt.Sprintf("in %d / out %d", usage.InputTokens, usage.OutputTokens),
	}
	if usage.Cost > 0 {
		lines = append(lines, fmt.Sprintf("$%.4f", usage.Cost))
	}
	if usage.EstimatedInputTokens > 0 {
		lines = append(lines, fmt.Sprintf("est in %d", usage.EstimatedInputTokens))
	}
	if usage.SavedEstimatedTokens > 0 {
		lines = append(lines, fmt.Sprintf("saved est %d", usage.SavedEstimatedTokens))
	}
	if usage.CompactedMessages > 0 || usage.CompactedToolResults > 0 {
		lines = append(lines, fmt.Sprintf("compact msg %d / tool %d", usage.CompactedMessages, usage.CompactedToolResults))
	}
	if usage.CacheHits > 0 || usage.CacheReadTokens > 0 || usage.CacheWriteTokens > 0 {
		lines = append(lines, fmt.Sprintf("cache hits %d", usage.CacheHits))
		lines = append(lines, fmt.Sprintf("cache read %d / write %d", usage.CacheReadTokens, usage.CacheWriteTokens))
	}
	return lines
}

func traceSummaryLines(summary runtime.TraceSummary) []string {
	name := firstNonEmpty(summary.TraceID, filepath.Base(summary.Path))
	lines := []string{name}
	lines = append(lines, fmt.Sprintf("%d events", summary.Events))
	if summary.ModelRequests > 0 {
		lines = append(lines, fmt.Sprintf("%d model calls", summary.ModelRequests))
	}
	if summary.SavedEstimatedTokens > 0 {
		lines = append(lines, fmt.Sprintf("saved est %d tokens", summary.SavedEstimatedTokens))
	}
	if summary.CompactedMessages > 0 || summary.CompactedToolResults > 0 {
		lines = append(lines, fmt.Sprintf("compact msg %d / tool %d", summary.CompactedMessages, summary.CompactedToolResults))
	}
	if summary.CacheHits > 0 || summary.CacheReadTokens > 0 || summary.CacheWriteTokens > 0 {
		lines = append(lines, fmt.Sprintf("cache hits %d", summary.CacheHits))
		lines = append(lines, fmt.Sprintf("cache read %d / write %d", summary.CacheReadTokens, summary.CacheWriteTokens))
	}
	if summary.ModelDurationMS > 0 || summary.ToolDurationMS > 0 || summary.RuntimeDurationMS > 0 {
		lines = append(lines, fmt.Sprintf("model %s", durationText(summary.ModelDurationMS)))
		lines = append(lines, fmt.Sprintf("tools %s", durationText(summary.ToolDurationMS)))
		lines = append(lines, fmt.Sprintf("runtime %s", durationText(summary.RuntimeDurationMS)))
	}
	return lines
}

func taskLines(state session.State) []string {
	task, ok := selectedTask(state)
	if !ok {
		if strings.TrimSpace(state.ActiveTask) != "" {
			return []string{"active " + state.ActiveTask}
		}
		return []string{"none"}
	}
	return taskSidebarLines(task)
}

func selectedTask(state session.State) (session.TaskNode, bool) {
	if strings.TrimSpace(state.ActiveTask) != "" {
		for _, task := range state.Tasks {
			if task.ID == state.ActiveTask {
				return task, true
			}
		}
		return session.TaskNode{}, false
	}
	if len(state.Tasks) == 0 {
		return session.TaskNode{}, false
	}
	return state.Tasks[len(state.Tasks)-1], true
}

func taskSidebarLines(task session.TaskNode) []string {
	status := firstNonEmpty(task.Status, task.Execution.Status, "latest")
	lines := []string{contractListTitle(status)}
	contract := task.Execution.Contract
	if contract == nil {
		return append(lines, "Goal: "+compactInline(firstNonEmpty(task.Summary, task.Goal, task.ID), 72))
	}
	goal := firstNonEmpty(contract.Summary, task.Summary, task.Goal)
	if goal != "" {
		lines = append(lines, contractChecklistLine("done", compactInline(goal, 84), "goal"))
	}
	if usefulContractText(contract.ExpectedOutcome) {
		lines = append(lines, contractChecklistLine("pending", compactInline(contract.ExpectedOutcome, 84), "outcome"))
	}
	if usefulContractText(contract.CompletionPolicy) {
		lines = append(lines, contractChecklistLine("pending", compactInline(contract.CompletionPolicy, 84), "done when"))
	}
	lines = append(lines, requirementLines(contract, task.Steps)...)
	return lines
}

func contractListTitle(status string) string {
	status = firstNonEmpty(status, "latest")
	return "▾ Contract " + status
}

func contractChecklistLine(state, text, suffix string) string {
	mark := "[ ]"
	color := ""
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "done", "accepted", "completed", "success":
		mark = "[✓]"
		color = ansiDim
	case "running", "active":
		mark = "[•]"
		color = ansiYellow
	case "failed", "blocked", "error":
		mark = "[!]"
		color = ansiRed
	}
	line := mark + " " + text
	if strings.TrimSpace(suffix) != "" {
		line += " — " + suffix
	}
	if color == "" {
		return line
	}
	return colorize(line, color, true)
}

func (m *tuiModel) fitSidebarLines(lines []string, maxLines, width int) []string {
	if maxLines <= 0 {
		return nil
	}
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, wrapLines(line, width)...)
	}
	if len(wrapped) <= maxLines {
		m.sidebarScroll = 0
		return wrapped
	}
	maxScroll := len(wrapped) - maxLines
	if m.sidebarScroll > maxScroll {
		m.sidebarScroll = maxScroll
	}
	if m.sidebarScroll < 0 {
		m.sidebarScroll = 0
	}
	out := append([]string{}, wrapped[m.sidebarScroll:m.sidebarScroll+maxLines]...)
	if m.sidebarScroll > 0 && len(out) > 0 {
		out[0] = colorize("... scroll up", ansiDim, true)
	}
	if m.sidebarScroll < maxScroll && len(out) > 0 {
		out[len(out)-1] = colorize("... scroll down", ansiDim, true)
	}
	return out
}

func appendSidebarOverflowHint(lines []string, maxLines int) []string {
	hint := colorize("... more in /events", ansiDim, true)
	if maxLines <= 1 {
		return []string{hint}
	}
	out := append([]string{}, lines[:maxLines-1]...)
	return append(out, hint)
}

func usefulContractText(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	generic := []string{
		"answer the user task",
		"final answer",
		"available context",
		"ask for required input",
		"cite the located previous task identifiers",
	}
	for _, marker := range generic {
		if strings.Contains(text, marker) {
			return false
		}
	}
	return true
}

func requirementLines(contract *session.TaskContract, steps []session.TaskStep) []string {
	if contract == nil {
		return nil
	}
	stats := contractToolStats(steps)
	var lines []string
	for _, tool := range contract.RequiredTools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		stat := stats[tool]
		lines = append(lines, contractChecklistLine(stat.State(), friendlyToolName(tool), stat.Suffix()))
	}
	for _, evidence := range contract.RequiredEvidence {
		label := firstNonEmpty(evidence.Description, evidence.Tool, evidence.Kind)
		if label == "" {
			continue
		}
		stat := contractItemStat{}
		if strings.TrimSpace(evidence.Tool) != "" {
			stat = stats[evidence.Tool]
		}
		lines = append(lines, contractChecklistLine(stat.State(), compactInline(label, 52), "evidence"))
	}
	return lines
}

type contractItemStat struct {
	Accepted bool
	Running  bool
	Failed   int
	Blocked  int
}

func (s contractItemStat) State() string {
	if s.Accepted {
		return "done"
	}
	if s.Running {
		return "running"
	}
	if s.Failed > 0 || s.Blocked > 0 {
		return "failed"
	}
	return "pending"
}

func (s contractItemStat) Suffix() string {
	failures := s.Failed + s.Blocked
	if s.Accepted && failures > 0 {
		if failures == 1 {
			return "after 1 retry"
		}
		return fmt.Sprintf("after %d retries", failures)
	}
	if !s.Accepted && failures > 0 {
		if failures == 1 {
			return "1 failed attempt"
		}
		return fmt.Sprintf("%d failed attempts", failures)
	}
	if s.Running {
		return "running"
	}
	return ""
}

func contractToolStats(steps []session.TaskStep) map[string]contractItemStat {
	out := map[string]contractItemStat{}
	for _, step := range steps {
		if strings.TrimSpace(step.Tool) == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(step.Status))
		stat := out[step.Tool]
		if step.Accepted || status == "accepted" || status == "completed" || status == "success" {
			stat.Accepted = true
		}
		if status == "running" {
			stat.Running = true
		}
		if status == "failed" || status == "error" {
			stat.Failed++
		}
		if status == "blocked" {
			stat.Blocked++
		}
		out[step.Tool] = stat
	}
	return out
}

func recentStepLines(steps []session.TaskStep, limit int) []string {
	if limit <= 0 || len(steps) == 0 {
		return nil
	}
	start := len(steps) - limit
	if start < 0 {
		start = 0
	}
	lines := []string{"Recent steps:"}
	for _, step := range steps[start:] {
		mark := "•"
		if step.Accepted || strings.EqualFold(step.Status, "accepted") || strings.EqualFold(step.Status, "completed") {
			mark = "✓"
		}
		if strings.EqualFold(step.Status, "blocked") || strings.EqualFold(step.Status, "failed") {
			mark = "!"
		}
		line := fmt.Sprintf("%s %s", mark, friendlyToolName(step.Tool))
		if strings.TrimSpace(step.Summary) != "" {
			line += " " + compactInline(step.Summary, 44)
		} else if strings.TrimSpace(step.Status) != "" {
			line += " " + strings.TrimSpace(step.Status)
		}
		lines = append(lines, line)
	}
	return lines
}

func (m *tuiModel) toolLines() []string {
	if strings.TrimSpace(m.lastTool) == "" {
		return []string{"none"}
	}
	return []string{friendlyToolName(m.lastTool), firstNonEmpty(m.lastToolState, "running")}
}

type commandPanelItem struct {
	Section  string
	Label    string
	Command  string
	What     string
	Example  string
	Shortcut string
	Direct   bool
	Action   string
}

func allCommandPanelItems() []commandPanelItem {
	return []commandPanelItem{
		{Section: "Suggested", Label: "New session", Command: "/new", What: "start a fresh task", Example: "/new", Shortcut: "ctrl+x n", Direct: true},
		{Section: "Suggested", Label: "Show trace", Command: "/trace", What: "summarize latest trace", Example: "/trace", Direct: true},
		{Section: "Suggested", Label: "Show events", Command: "/events", What: "render process events", Example: "/events", Direct: true},
		{Section: "Suggested", Label: "Switch model", Command: "/model", What: "open model selector", Example: "/model", Shortcut: "ctrl+x m", Direct: true},
		{Section: "Suggested", Label: "Manage tools", Command: "/tools", What: "open tool selector", Example: "/tools", Direct: true},

		{Section: "Session", Label: "Switch session", Command: "/sessions", What: "choose an existing CLI, Feishu, or Weixin session", Example: "/sessions", Direct: true, Action: "sessions"},
		{Section: "Session", Label: "Resume from session", Command: "/sessions", What: "choose a session first, then continue from it", Example: "/sessions", Direct: true, Action: "sessions"},
		{Section: "Session", Label: "Show session", Command: "/show", What: "show messages, tasks, and active task", Example: "/show", Direct: true},

		{Section: "Observe", Label: "Trace summary", Command: "/trace [path|key]", What: "summarize trace or session", Example: "/trace"},
		{Section: "Observe", Label: "Process events", Command: "/events [path|key]", What: "show model/tool/final events", Example: "/events"},
		{Section: "Observe", Label: "Raw events JSON", Command: "/events --json", What: "show trace events as JSON", Example: "/events --json", Direct: true},

		{Section: "Agent / Model", Label: "Switch model", Command: "/model", What: "open model selector", Example: "/model", Direct: true},
		{Section: "Agent / Model", Label: "Model details", Command: "/model --verbose", What: "show selection chain and endpoints", Example: "/model --verbose", Direct: true},

		{Section: "Tools", Label: "Manage tools", Command: "/tools", What: "open tool enable/disable selector", Example: "/tools", Direct: true},
		{Section: "Tools", Label: "Tool details", Command: "/tools --verbose", What: "show arguments and boundaries", Example: "/tools --verbose", Direct: true},
		{Section: "Tools", Label: "Enable or disable tool", Command: "/tools", What: "choose a tool from the selector", Example: "/tools", Direct: true, Action: "tools"},

		{Section: "Channel", Label: "Choose channel session", Command: "/sessions", What: "switch to a Feishu or Weixin session already known locally", Example: "/sessions", Direct: true, Action: "sessions"},

		{Section: "Memory", Label: "Memory proposals", Command: "/memory proposals", What: "show how to review pending memory changes", Example: "/memory proposals", Direct: true, Action: "memory_proposals"},

		{Section: "Local", Label: "Workspace report", Command: "/workspace", What: "show local runtime paths and current workspace", Example: "/workspace", Direct: true, Action: "workspace"},
		{Section: "Local", Label: "Gateway status", Command: "/gateway", What: "show local gateway status command", Example: "/gateway", Direct: true, Action: "gateway"},
		{Section: "Local", Label: "Help", Command: "/help", What: "show full command guide", Example: "/help", Direct: true},
		{Section: "Local", Label: "Exit", Command: "/exit", What: "leave the TUI", Example: "/exit", Direct: true},
	}
}

func (m *tuiModel) filteredCommandItems() []commandPanelItem {
	items := allCommandPanelItems()
	filter := strings.ToLower(m.commandSearch())
	if filter == "" || filter == "/" {
		return items
	}
	var out []commandPanelItem
	for _, item := range items {
		haystack := strings.ToLower(strings.Join([]string{item.Section, item.Label, item.Command, item.What, item.Example}, " "))
		if strings.Contains(haystack, filter) {
			out = append(out, item)
		}
	}
	return out
}

func (m *tuiModel) commandSearch() string {
	value := strings.TrimSpace(m.input.Value())
	value = strings.TrimPrefix(value, "/")
	return strings.TrimSpace(value)
}

func (m *tuiModel) updateCommandPanel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.commandPanel = false
		if strings.TrimSpace(m.input.Value()) == "/" {
			m.input.SetValue("")
		}
		return m, nil
	case "up":
		m.commandIndex--
		if m.commandIndex < 0 {
			m.commandIndex = len(m.filteredCommandItems()) - 1
		}
		return m, nil
	case "down":
		m.commandIndex++
		items := m.filteredCommandItems()
		if len(items) == 0 || m.commandIndex >= len(items) {
			m.commandIndex = 0
		}
		return m, nil
	case "enter":
		items := m.filteredCommandItems()
		if len(items) == 0 {
			m.commandPanel = false
			return m, nil
		}
		if m.commandIndex < 0 || m.commandIndex >= len(items) {
			m.commandIndex = 0
		}
		item := items[m.commandIndex]
		if cmd := m.runCommandPanelAction(item); cmd != nil {
			m.commandPanel = false
			m.input.SetValue("")
			return m, cmd
		}
		value := commandFillValue(item)
		m.commandPanel = false
		m.input.SetValue(value)
		if item.Direct && strings.HasPrefix(value, "/") {
			return m, m.submit()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if !strings.HasPrefix(strings.TrimSpace(m.input.Value()), "/") {
		m.commandPanel = false
	}
	m.commandIndex = 0
	return m, cmd
}

func commandFillValue(item commandPanelItem) string {
	if strings.TrimSpace(item.Example) != "" && strings.HasPrefix(strings.TrimSpace(item.Example), "/") {
		return strings.TrimSpace(item.Example)
	}
	if strings.TrimSpace(item.Example) != "" && !strings.Contains(item.Example, "<") {
		return strings.TrimSpace(item.Example)
	}
	if strings.Contains(item.Command, "<") || strings.Contains(item.Command, "[") {
		return item.Command
	}
	fields := strings.Fields(item.Command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (m *tuiModel) recordHistory(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != text {
		m.history = append(m.history, text)
	}
	m.historyIndex = -1
}

func (m *tuiModel) historyUp() {
	if len(m.history) == 0 {
		return
	}
	if m.historyIndex < 0 {
		m.historyIndex = len(m.history) - 1
	} else if m.historyIndex > 0 {
		m.historyIndex--
	}
	m.input.SetValue(m.history[m.historyIndex])
}

func (m *tuiModel) historyDown() {
	if len(m.history) == 0 || m.historyIndex < 0 {
		return
	}
	if m.historyIndex >= len(m.history)-1 {
		m.historyIndex = -1
		m.input.SetValue("")
		return
	}
	m.historyIndex++
	m.input.SetValue(m.history[m.historyIndex])
}

func durationText(ms int64) string {
	if ms <= 0 {
		return "0ms"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func sidebarStyle(width, height int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(lipgloss.Color("238")).
		Padding(1, 2).
		Width(width).
		Height(maxInt(1, height))
}

func (m *tuiModel) submit() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	m.commandPanel = false
	if m.running {
		m.addEvent(colorize("task is still running; draft kept in input", ansiYellow, true))
		return nil
	}
	m.input.SetValue("")
	m.recordHistory(text)
	if cmd, ok := ParseSlash(text); ok {
		if m.handleSlash(cmd) {
			return tea.Quit
		}
		return nil
	}
	m.running = true
	m.status = "Thinking"
	m.progress = 0
	m.toolEvents = 0
	m.currentTask = text
	m.liveSteps = nil
	m.addEvent(colorize("User", ansiCyan, true))
	m.addEvent("│ " + compactBlock(text, 600))
	m.addEvent("")
	m.addEvent(colorize("• Thinking", ansiDim, true))
	return m.runTaskCmd(text)
}

func (m *tuiModel) runTaskCmd(text string) tea.Cmd {
	return func() tea.Msg {
		go m.runTask(text)
		return nil
	}
}

func (m *tuiModel) runCommandPanelAction(item commandPanelItem) tea.Cmd {
	switch item.Action {
	case "sessions":
		if err := m.openSessionsPicker(); err != nil {
			m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
		}
	case "models":
		m.openModelsPicker()
	case "tools":
		m.openToolsPicker()
	case "memory_proposals":
		m.showMemoryProposalHelp()
	case "workspace":
		m.showWorkspaceSummary()
	case "gateway":
		m.showGatewayStatusHelp()
	default:
		return nil
	}
	return func() tea.Msg { return nil }
}

func (m *tuiModel) runTask(text string) {
	rt := runtime.New(m.cfg)
	rt.ProgressSink = func(msg channel.OutboundMessage) {
		if len(msg.Progress) == 0 || m.program == nil {
			return
		}
		step := msg.Progress[len(msg.Progress)-1]
		m.program.Send(tuiProgressMsg{
			line:      renderTUIProgressBlock(step),
			status:    progressStatus(step),
			tool:      step.Tool,
			toolState: step.Status,
			summary:   step.Summary,
			duration:  step.DurationMS,
		})
	}
	resp, err := rt.Handle(m.ctx, inbound(text, m.sessionKey))
	if m.program != nil {
		m.program.Send(tuiResultMsg{reply: resp.Reply, followUps: resp.FollowUps, tracePath: resp.TracePath, err: err})
	}
}

func (m *tuiModel) recordLiveStep(msg tuiProgressMsg) {
	toolName := strings.TrimSpace(msg.tool)
	if toolName == "" {
		return
	}
	status := firstNonEmpty(msg.toolState, "running")
	summary := strings.TrimSpace(msg.summary)
	if summary == "" && msg.duration > 0 {
		summary = durationText(msg.duration)
	}
	step := session.TaskStep{
		Tool:     toolName,
		Status:   status,
		Summary:  summary,
		Accepted: strings.EqualFold(status, "accepted") || strings.EqualFold(status, "completed") || strings.EqualFold(status, "success"),
	}
	if len(m.liveSteps) > 0 {
		last := &m.liveSteps[len(m.liveSteps)-1]
		if last.Tool == step.Tool && (last.Status == "running" || last.Summary == step.Summary || step.Accepted) {
			*last = step
			return
		}
	}
	m.liveSteps = append(m.liveSteps, step)
	if len(m.liveSteps) > 12 {
		m.liveSteps = m.liveSteps[len(m.liveSteps)-12:]
	}
}

func (m *tuiModel) addEvent(line string) {
	if len(m.events) > 0 && m.events[len(m.events)-1] == line {
		return
	}
	m.events = append(m.events, line)
	if len(m.events) > maxTUIEventLines {
		drop := len(m.events) - maxTUIEventLines + 1
		m.events = append([]string{colorize(fmt.Sprintf("... trimmed %d older TUI lines; use /events or /trace for full history", drop), ansiDim, true)}, m.events[drop:]...)
	}
	if !m.autoFollow {
		m.newEvents++
	}
	m.refreshViewport()
}

func (m *tuiModel) addOutput(text string) {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		m.addEvent(line)
	}
}

func (m *tuiModel) refreshViewport() {
	m.viewport.SetContent(strings.Join(m.events, "\n"))
	if m.autoFollow {
		m.viewport.GotoBottom()
		m.newEvents = 0
	}
}

func (m *tuiModel) addSessionBanner() {
	state, err := session.NewStore(m.cfg.App.Home).Load(m.sessionKey)
	label := m.sessionDisplayName(state)
	if err == nil && (len(state.Messages) > 0 || len(state.Tasks) > 0) {
		m.addEvent(colorize(fmt.Sprintf("Session resumed - %s (%d messages, %d tasks)", label, len(state.Messages), len(state.Tasks)), ansiDim, true))
		if last := lastDisplayMessage(state); last != "" {
			m.addEvent(colorize("Last message", ansiDim, true))
			m.addEvent("│ " + compactBlock(last, 220))
		}
		return
	}
	m.addEvent(colorize("Session ready - "+label+" - "+time.Now().Format("2006-01-02 15:04:05"), ansiDim, true))
}

func lastDisplayMessage(state session.State) string {
	for i := len(state.Messages) - 1; i >= 0; i-- {
		msg := state.Messages[i]
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		switch string(msg.Role) {
		case "user":
			return "User: " + msg.Content
		case "assistant":
			return "Assistant: " + msg.Content
		}
	}
	return ""
}

func (m *tuiModel) handleSlash(cmd SlashCommand) bool {
	switch cmd.Name {
	case "exit", "quit", "q":
		return true
	case "help", "?":
		var out bytes.Buffer
		printChatHelp(&out)
		m.addOutput(out.String())
	case "new":
		m.running = true
		m.status = "Thinking"
		m.progress = 0
		m.toolEvents = 0
		m.currentTask = "/new"
		m.liveSteps = nil
		m.addEvent(colorize("User", ansiCyan, true))
		m.addEvent("│ /new")
		m.addEvent("")
		m.addEvent(colorize("• Thinking", ansiDim, true))
		go m.runTask("/new")
	case "session":
		if len(cmd.Args) == 0 {
			if err := m.openSessionsPicker(); err != nil {
				m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			}
			return false
		}
		next := ResolveSessionKey(cmd.Args[0])
		m.sessionKey = next
		m.addEvent("session: " + next)
	case "sessions":
		if len(cmd.Args) == 0 {
			if err := m.openSessionsPicker(); err != nil {
				m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			}
			return false
		}
		keys, err := session.NewStore(m.cfg.App.Home).List()
		if err != nil {
			m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		sort.Strings(keys)
		m.addEvent(colorize("Sessions", ansiGreen, true))
		for _, key := range keys {
			prefix := "  "
			if key == m.sessionKey {
				prefix = "* "
			}
			m.addEvent(prefix + key)
		}
	case "show":
		key := m.sessionKey
		if len(cmd.Args) > 0 {
			key = ResolveSessionKey(cmd.Args[0])
		}
		state, err := session.NewStore(m.cfg.App.Home).Load(key)
		if err != nil {
			m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		m.addEvent(colorize("Session", ansiGreen, true))
		m.addEvent(fmt.Sprintf("key=%s messages=%d tasks=%d active=%s", state.Key, len(state.Messages), len(state.Tasks), state.ActiveTask))
	case "trace":
		path, err := m.resolveTracePath(cmd.Args)
		if err != nil {
			m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		var out bytes.Buffer
		if err := printTraceSummary(&out, path); err != nil {
			m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		m.addOutput(out.String())
	case "events":
		opts := TraceEventsOptions{}
		var filtered []string
		for _, arg := range cmd.Args {
			if arg == "--json" {
				opts.JSON = true
				continue
			}
			filtered = append(filtered, arg)
		}
		path, err := m.resolveTracePath(filtered)
		if err != nil {
			m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		var out bytes.Buffer
		fmt.Fprintln(&out, "trace:", path)
		if err := PrintTraceEventsWithOptions(&out, path, opts); err != nil {
			m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		m.addOutput(out.String())
	case "tools":
		if len(cmd.Args) == 0 {
			m.openToolsPicker()
			return false
		}
		var out bytes.Buffer
		if err := m.handleToolsSlash(&out, cmd.Args); err != nil {
			m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		m.addOutput(out.String())
	case "model":
		if len(cmd.Args) == 0 {
			m.openModelsPicker()
			return false
		}
		verbose, agentID, err := parseModelSlashArgs(cmd.Args)
		if err != nil {
			m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		var out bytes.Buffer
		if err := PrintModel(&out, m.cfg, agentID, verbose); err != nil {
			m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		m.addOutput(out.String())
	case "resume":
		if len(cmd.Args) == 0 {
			if err := m.openSessionsPicker(); err != nil {
				m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			}
			return false
		}
		if err := m.resume(cmd.Args); err != nil {
			m.addEvent(colorize("error: "+err.Error(), ansiRed, true))
		}
	case "memory":
		if len(cmd.Args) > 0 && cmd.Args[0] == "proposals" {
			m.showMemoryProposalHelp()
		} else {
			m.addEvent(colorize("usage: /memory proposals", ansiYellow, true))
		}
	case "workspace":
		m.showWorkspaceSummary()
	case "gateway":
		m.showGatewayStatusHelp()
	default:
		m.addEvent(colorize("unknown command: /"+cmd.Name, ansiYellow, true))
	}
	return false
}

func (m *tuiModel) openSessionsPicker() error {
	store := session.NewStore(m.cfg.App.Home)
	keys, err := store.List()
	if err != nil {
		return err
	}
	sort.Strings(keys)
	items := make([]tuiPickerItem, 0, len(keys))
	for _, key := range keys {
		state, _ := store.Load(key)
		status := ""
		if key == m.sessionKey {
			status = "current"
		}
		detail := fmt.Sprintf("%d messages · %d tasks", len(state.Messages), len(state.Tasks))
		if !state.UpdatedAt.IsZero() {
			detail += " · " + state.UpdatedAt.Format("01-02 15:04")
		}
		items = append(items, tuiPickerItem{
			Label:  key,
			Detail: detail,
			Value:  key,
			Status: status,
		})
	}
	if len(items) == 0 {
		items = []tuiPickerItem{{Label: m.sessionKey, Detail: "current session", Value: m.sessionKey, Status: "current"}}
	}
	m.picker = &tuiPicker{Kind: "sessions", Title: "Sessions", Items: items}
	return nil
}

func (m *tuiModel) showMemoryProposalHelp() {
	m.addEvent(colorize("Memory proposals", ansiGreen, true))
	m.addEvent("review pending memory changes from a shell:")
	m.addEvent("  mateway memory proposal list")
	m.addEvent("  mateway memory proposal show <proposal_id>")
	m.addEvent("  mateway memory proposal commit <proposal_id>")
	m.addEvent("  mateway memory proposal reject <proposal_id> --reason \"...\"")
}

func (m *tuiModel) showWorkspaceSummary() {
	m.addEvent(colorize("Workspace", ansiGreen, true))
	m.addEvent("home: " + firstNonEmpty(m.cfg.App.Home, "~/.mateway"))
	if wd, err := os.Getwd(); err == nil {
		m.addEvent("cwd:  " + wd)
	}
	m.addEvent("details: mateway workspace report")
}

func (m *tuiModel) showGatewayStatusHelp() {
	m.addEvent(colorize("Gateway", ansiGreen, true))
	m.addEvent("check local gateway from a shell:")
	m.addEvent("  mateway gateway status")
}

func (m *tuiModel) openModelsPicker() {
	var items []tuiPickerItem
	current := strings.TrimSpace(m.model.Name)
	for _, model := range m.cfg.Models {
		if !model.Enabled {
			continue
		}
		status := ""
		if strings.EqualFold(model.Name, current) || strings.EqualFold(model.Model, m.model.Model) {
			status = "current"
		}
		detail := firstNonEmpty(model.Model, model.Provider)
		if model.Provider != "" && model.Model != "" {
			detail = model.Provider + " · " + model.Model
		}
		if model.ContextWindow > 0 {
			detail += fmt.Sprintf(" · %d ctx", model.ContextWindow)
		}
		items = append(items, tuiPickerItem{
			Label:  model.Name,
			Detail: detail,
			Value:  model.Name,
			Status: status,
		})
	}
	if len(items) == 0 {
		items = []tuiPickerItem{{Label: m.model.Display(), Detail: "configured fallback", Value: m.model.Name, Status: "current"}}
	}
	m.picker = &tuiPicker{Kind: "models", Title: "Models", Items: items}
}

func (m *tuiModel) openToolsPicker() {
	all := tool.NewRegistry(m.cfg).List()
	enabled := map[string]bool{}
	profile, hasProfile := findAgentProfile(m.cfg, firstNonEmpty(m.agent.ID, m.cfg.Agents.Default))
	if hasProfile {
		for _, item := range tool.NewRegistryForProfile(m.cfg, profile).List() {
			enabled[item.Name()] = true
		}
	} else {
		for _, item := range all {
			enabled[item.Name()] = true
		}
	}
	items := make([]tuiPickerItem, 0, len(all))
	for _, item := range all {
		status := "enabled"
		if !enabled[item.Name()] {
			status = "disabled"
		}
		required := strings.Join(item.Schema().Required, ",")
		if required == "" {
			required = "no required args"
		}
		items = append(items, tuiPickerItem{
			Label:  item.Name(),
			Detail: fmt.Sprintf("%s · %s", item.Risk(), required),
			Value:  item.Name(),
			Status: status,
		})
	}
	m.picker = &tuiPicker{Kind: "tools", Title: "Tools", Items: items}
}

func (m *tuiModel) handleToolsSlash(out io.Writer, args []string) error {
	verbose := false
	agentID := ""
	var values []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--verbose":
			verbose = true
		case "--agent":
			if i+1 >= len(args) {
				return fmt.Errorf("usage: /tools [--agent <agent_id>] [--verbose]")
			}
			i++
			agentID = args[i]
		default:
			values = append(values, args[i])
		}
	}
	if len(values) == 0 {
		return PrintTools(out, m.cfg, agentID, verbose)
	}
	if len(values) != 2 {
		return fmt.Errorf("usage: /tools [enable|disable] <tool_name> [--agent <agent_id>]")
	}
	var (
		change ToolAccessChange
		err    error
	)
	switch values[0] {
	case "enable":
		change, err = EnableTool(m.cfg, agentID, values[1])
	case "disable":
		change, err = DisableTool(m.cfg, agentID, values[1])
	default:
		return fmt.Errorf("usage: /tools [enable|disable] <tool_name> [--agent <agent_id>]")
	}
	if err != nil {
		return err
	}
	PrintToolAccessChange(out, change)
	return nil
}

func (m *tuiModel) resolveTracePath(args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("usage: /trace [trace_path|session_key]")
	}
	store := session.NewStore(m.cfg.App.Home)
	if len(args) == 1 {
		value := strings.TrimSpace(args[0])
		if strings.Contains(value, "/") || strings.HasSuffix(value, ".jsonl") {
			return value, nil
		}
		key := ResolveSessionKey(value)
		state, err := store.Load(key)
		if err != nil {
			return "", err
		}
		if path := latestTracePath(state); path != "" {
			return path, nil
		}
		return "", fmt.Errorf("session %q has no trace", key)
	}
	state, err := store.Load(m.sessionKey)
	if err != nil {
		return "", err
	}
	if path := latestTracePath(state); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("session %q has no trace", m.sessionKey)
}

func (m *tuiModel) resume(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: /resume [--attach] <session_key>")
	}
	attach := false
	var values []string
	for _, arg := range args {
		switch arg {
		case "--attach":
			attach = true
		default:
			values = append(values, arg)
		}
	}
	if len(values) != 1 {
		return fmt.Errorf("usage: /resume [--attach] <session_key>")
	}
	source := ResolveSessionKey(values[0])
	if attach {
		m.sessionKey = source
		m.addEvent("attached: " + source)
		return nil
	}
	target := m.sessionKey
	if err := ForkSession(session.NewStore(m.cfg.App.Home), source, target); err != nil {
		return err
	}
	m.addEvent(fmt.Sprintf("resumed %s into %s", source, target))
	return nil
}

func progressStatus(step channel.ProgressStep) string {
	if strings.TrimSpace(step.Tool) != "" {
		switch strings.TrimSpace(step.Status) {
		case "completed", "success", "accepted", "done":
			return "Done"
		case "blocked", "failed", "error":
			return "Blocked"
		default:
			return "Acting: " + friendlyToolName(step.Tool)
		}
	}
	if strings.EqualFold(step.Status, "thinking") {
		return "Thinking"
	}
	return firstNonEmpty(step.Status, "Acting")
}

func renderTUIProgressBlock(step channel.ProgressStep) string {
	event := eventFromProgressStep(step)
	label, detail := processEventLabelAndDetail(event)
	if strings.TrimSpace(label) == "" {
		return ""
	}
	if event.Type != "tool.started" && event.Type != "tool.completed" && event.Type != "tool.blocked" && event.Type != "tool.progress" {
		return renderProcessEvent(event, true)
	}
	color := processEventColor(event)
	title := friendlyToolName(firstNonEmpty(event.Tool, event.Title))
	status := firstNonEmpty(event.Status, strings.TrimSpace(label))
	lines := []string{colorize("┌ Tool "+title, color, true)}
	if strings.TrimSpace(event.Args) != "" {
		lines = append(lines, "│ args: "+compactInline(event.Args, 96))
	}
	if strings.TrimSpace(event.Summary) != "" {
		lines = append(lines, "│ summary: "+compactInline(event.Summary, 120))
	}
	if event.DurationMS > 0 {
		lines = append(lines, "│ duration: "+durationText(event.DurationMS))
	}
	if strings.TrimSpace(detail) != "" && event.Args == "" && event.Summary == "" {
		lines = append(lines, "│ "+compactInline(detail, 120))
	}
	if event.TimedOut {
		lines = append(lines, "│ timed out")
	}
	lines = append(lines, colorize("└ "+status, color, true))
	return strings.Join(lines, "\n")
}

func statusRight(width int, parts []string) string {
	visible := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			visible = append(visible, colorize(part, ansiDim, true))
		}
	}
	if width < 80 && len(visible) > 2 {
		visible = visible[len(visible)-2:]
	}
	if width < 120 && len(visible) > 3 {
		visible = visible[len(visible)-3:]
	}
	return strings.Join(visible, "  ")
}

type tuiModelInfo struct {
	Name     string
	Provider string
	Model    string
}

func (m tuiModelInfo) Display() string {
	return firstNonEmpty(m.Model, m.Name, "model:unknown")
}

func currentTUIModel(cfg *config.Root, agent config.AgentProfileConfig) tuiModelInfo {
	if cfg == nil {
		return tuiModelInfo{}
	}
	for _, name := range modelChain(agent.Model, cfg.Model) {
		for _, model := range cfg.Models {
			if !model.Enabled {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(model.Name), strings.TrimSpace(name)) {
				return tuiModelInfo{Name: model.Name, Provider: model.Provider, Model: model.Model}
			}
		}
	}
	name := firstNonEmpty(agent.Model.Default, cfg.Model.Default)
	return tuiModelInfo{Name: name, Model: name}
}

func wrapLines(text string, width int) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.Contains(line, "\x1b[") {
			out = append(out, truncateANSI(line, width))
			continue
		}
		for visibleLen(line) > width && width > 0 {
			head := truncateANSI(line, width)
			out = append(out, head)
			line = strings.TrimPrefix(line, head)
		}
		out = append(out, line)
	}
	return out
}

func truncateANSI(text string, width int) string {
	if width <= 0 || visibleLen(text) <= width {
		return text
	}
	var b strings.Builder
	visible := 0
	inEscape := false
	for _, r := range text {
		if r == '\x1b' {
			inEscape = true
			b.WriteRune(r)
			continue
		}
		if inEscape {
			b.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		w := runewidth.RuneWidth(r)
		if visible+w > width {
			break
		}
		b.WriteRune(r)
		visible += w
	}
	if !inEscape && strings.Contains(text, "\x1b[") {
		b.WriteString(ansiReset)
	}
	return b.String()
}

func visibleLen(text string) int {
	n := 0
	inEscape := false
	for _, r := range text {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		n += runewidth.RuneWidth(r)
	}
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
