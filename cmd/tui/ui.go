package main

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type state int

const (
	stateConfig state = iota
	stateRunning
	stateResults
)

type crawlerResult struct {
	Language  string
	ReqPerSec float64
	TimeTaken time.Duration
}

type model struct {
	state       state
	spinner     spinner.Model
	resultsFunc func() tea.Msg
	results     []crawlerResult
	table       table.Model

	// current benchmark index when running
	currentIdx int
	crawlers   []string

	err error
}

type benchmarkCompleteMsg struct {
	result crawlerResult
}

type allBenchmarksCompleteMsg struct{}

func initialModel() model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	columns := []table.Column{
		{Title: "Language", Width: 15},
		{Title: "Req/s", Width: 15},
		{Title: "Time", Width: 15},
	}
	t := table.New(
		table.WithColumns(columns),
		table.WithFocused(true),
		table.WithHeight(7),
	)

	return model{
		state:    stateConfig,
		spinner:  s,
		crawlers: []string{"Go", "TypeScript", "Rust"},
		table:    t,
	}
}

func (m model) Init() tea.Cmd {
	return m.spinner.Tick
}

func runStubBenchmark(language string) tea.Cmd {
	return func() tea.Msg {
		// Simulate running a crawler for 2-3 seconds
		time.Sleep(time.Duration(rand.Intn(2000)+1000) * time.Millisecond)
		return benchmarkCompleteMsg{
			result: crawlerResult{
				Language:  language,
				ReqPerSec: 1000 + rand.Float64()*5000,
				TimeTaken: 2 * time.Second,
			},
		}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if m.state == stateConfig {
				m.state = stateRunning
				m.currentIdx = 0
				return m, runStubBenchmark(m.crawlers[m.currentIdx])
			}
		}

	case benchmarkCompleteMsg:
		m.results = append(m.results, msg.result)
		m.currentIdx++
		if m.currentIdx < len(m.crawlers) {
			return m, runStubBenchmark(m.crawlers[m.currentIdx])
		} else {
			m.state = stateResults
			m.updateTableData()
			return m, nil
		}

	case spinner.TickMsg:
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *model) updateTableData() {
	rows := []table.Row{}
	for _, r := range m.results {
		rows = append(rows, table.Row{
			r.Language,
			fmt.Sprintf("%.2f", r.ReqPerSec),
			r.TimeTaken.String(),
		})
	}
	m.table.SetRows(rows)
}

func (m model) View() string {
	var s string

	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("205")).
		Render("🕷️  Open-Crawl Benchmark Harness\n")

	s += header + "\n"

	switch m.state {
	case stateConfig:
		s += "Press [ENTER] to start the benchmark suite.\n\n"
		s += lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Crawlers to run: Go, TypeScript, Rust\nTarget: http://localhost:8080/page/1\nMax Requests: 10,000\nConcurrency: 100")
	case stateRunning:
		s += fmt.Sprintf("%s Running %s crawler...\n", m.spinner.View(), m.crawlers[m.currentIdx])
	case stateResults:
		s += "✅ Benchmarks Complete!\n\n"
		s += m.table.View()
	}

	s += "\n\nPress 'q' or 'ctrl+c' to quit.\n"

	return lipgloss.NewStyle().Margin(1, 2).Render(s)
}
