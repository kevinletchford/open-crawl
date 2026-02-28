package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
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

type progressMsg struct {
	reqs int64
}

type model struct {
	state        state
	spinner      spinner.Model
	results      []crawlerResult
	table        table.Model
	progressChan chan progressMsg
	currentReqs  int64

	// current benchmark index when running
	currentIdx int
	crawlers   []string
}

type benchmarkCompleteMsg struct {
	result crawlerResult
}

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
		state:        stateConfig,
		spinner:      s,
		crawlers:     []string{"Go", "TypeScript", "Rust"},
		table:        t,
		progressChan: make(chan progressMsg, 1000), // Buffered to handle rapid prints
	}
}

func (m model) Init() tea.Cmd {
	return m.spinner.Tick
}

func waitForProgress(c <-chan progressMsg) tea.Cmd {
	return func() tea.Msg {
		return <-c
	}
}

func runRealBenchmark(language string, progChan chan<- progressMsg) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd

		switch language {
		case "Go":
			cmd = exec.Command("./bin/crawler-go", "--url=http://localhost:8080/page/1", "--max=10000", "-c=100")
		case "TypeScript":
			cmd = exec.Command("node", "index.js", "--url=http://localhost:8080/page/1", "--max=10000", "-c=100")
			cmd.Dir = "./cmd/crawler-ts"
		case "Rust":
			cmd = exec.Command("../../bin/crawler-rust", "--url=http://localhost:8080/page/1", "--max=10000", "-c=100")
			cmd.Dir = "./cmd/crawler-rust"
		default:
			return benchmarkCompleteMsg{
				result: crawlerResult{Language: language, ReqPerSec: 0, TimeTaken: 0},
			}
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			return benchmarkCompleteMsg{
				result: crawlerResult{Language: language + " (Pipe Err)", ReqPerSec: 0, TimeTaken: 0},
			}
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return benchmarkCompleteMsg{
				result: crawlerResult{Language: language + " (Pipe Err)", ReqPerSec: 0, TimeTaken: 0},
			}
		}

		if err := cmd.Start(); err != nil {
			return benchmarkCompleteMsg{
				result: crawlerResult{Language: language + " (Start Err)", ReqPerSec: 0, TimeTaken: 0},
			}
		}

		// Read stderr for progress without blocking stdout
		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "PROGRESS: ") {
					valStr := strings.TrimSpace(strings.TrimPrefix(line, "PROGRESS: "))
					val, err := strconv.ParseInt(valStr, 10, 64)
					if err == nil {
						select {
						case progChan <- progressMsg{reqs: val}:
						default:
							// Drop if too fast
						}
					}
				}
			}
		}()

		out, err := io.ReadAll(stdout)
		if err != nil {
			return benchmarkCompleteMsg{result: crawlerResult{Language: language + " (IO Err)"}}
		}
		cmd.Wait()

		var res BenchmarkResult
		if err := json.Unmarshal(out, &res); err != nil {
			return benchmarkCompleteMsg{
				result: crawlerResult{
					Language:  language + " (Parse Err)",
					ReqPerSec: 0,
					TimeTaken: 0,
				},
			}
		}

		return benchmarkCompleteMsg{
			result: crawlerResult{
				Language:  res.Language,
				ReqPerSec: res.ReqPerSec,
				TimeTaken: time.Duration(res.TimeTakenMs) * time.Millisecond,
			},
		}
	}
}

// Ensure we have a matching type for the JSON payload
type BenchmarkResult struct {
	Language    string  `json:"language"`
	Requests    int64   `json:"requests"`
	TimeTakenMs int64   `json:"time_taken_ms"`
	ReqPerSec   float64 `json:"req_per_sec"`
	BytesRead   int64   `json:"bytes_read"`
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
				m.currentReqs = 0
				return m, tea.Batch(
					runRealBenchmark(m.crawlers[m.currentIdx], m.progressChan),
					waitForProgress(m.progressChan),
				)
			}
		}

	case progressMsg:
		m.currentReqs = msg.reqs
		return m, waitForProgress(m.progressChan)

	case benchmarkCompleteMsg:
		m.results = append(m.results, msg.result)
		m.currentIdx++
		m.currentReqs = 0
		if m.currentIdx < len(m.crawlers) {
			return m, runRealBenchmark(m.crawlers[m.currentIdx], m.progressChan)
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

func (m model) progressBar(current, total int) string {
	width := 40
	if current > total {
		current = total
	}
	if current < 0 {
		current = 0
	}
	percent := float64(current) / float64(total)
	filled := int(percent * float64(width))
	if filled > width {
		filled = width
	}
	empty := width - filled

	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(strings.Repeat("█", filled))
	bar += lipgloss.NewStyle().Foreground(lipgloss.Color("237")).Render(strings.Repeat("█", empty))
	return bar
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
		bar := m.progressBar(int(m.currentReqs), 10000)
		s += fmt.Sprintf("%s Running %s crawler...\n\n%s %d/10000 requests",
			m.spinner.View(), m.crawlers[m.currentIdx], bar, m.currentReqs)
	case stateResults:
		s += "✅ Benchmarks Complete!\n\n"
		s += m.table.View()
	}

	s += "\n\nPress 'q' or 'ctrl+c' to quit.\n"

	return lipgloss.NewStyle().Margin(1, 2).Render(s)
}
