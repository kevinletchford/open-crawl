package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"database/sql"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	_ "github.com/mattn/go-sqlite3"
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
	reqs   int64
	recent []string
}

type model struct {
	state        state
	spinner      spinner.Model
	results      []crawlerResult
	table        table.Model
	progressChan chan progressMsg
	currentReqs  int64
	recentURLs   []string

	// current benchmark index when running
	currentIdx  int
	currentPass int
	totalPasses int
	maxURLs     int
	targetURL   string
	crawlers    []string

	inputs     []textinput.Model
	focusIndex int

	urlHistory   []string
	historyIndex int

	showHelp bool
}

type benchmarkCompleteMsg struct {
	result crawlerResult
}

// Global DB connection
var db *sql.DB

func initDB() {
	dbPath := "./exports/benchmarks.db"

	var dbErr error
	db, dbErr = sql.Open("sqlite3", dbPath)
	if dbErr != nil {
		fmt.Printf("Failed to open database: %v\n", dbErr)
		return
	}

	createTableSQL := `CREATE TABLE IF NOT EXISTS benchmarks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		target_url TEXT,
		language TEXT,
		requests INTEGER,
		time_taken_ms INTEGER,
		req_per_sec REAL,
		bytes_read INTEGER
	);`

	var err error
	_, err = db.Exec(createTableSQL)
	if err != nil {
		fmt.Printf("Failed to initialize database: %v\n", err)
	}
}

func saveBenchmarkToDB(targetURL string, res BenchmarkResult) {
	if db == nil {
		return
	}
	insertSQL := `INSERT INTO benchmarks(target_url, language, requests, time_taken_ms, req_per_sec, bytes_read) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := db.Exec(insertSQL, targetURL, res.Language, res.Requests, res.TimeTakenMs, res.ReqPerSec, res.BytesRead)
	if err != nil {
		fmt.Printf("Failed to save benchmark: %v\n", err)
	}
}

func getHistoricalAverage(targetURL string, language string) (float64, error) {
	if db == nil {
		return 0, fmt.Errorf("DB not initialized")
	}
	query := `SELECT AVG(req_per_sec) FROM benchmarks WHERE target_url = ? AND language LIKE ?`
	// Use LIKE to match Base Language (e.g. Go (Parse Err) -> Go%)
	var avgReqPerSec sql.NullFloat64
	err := db.QueryRow(query, targetURL, language+"%").Scan(&avgReqPerSec)
	if err != nil {
		return 0, err
	}
	if !avgReqPerSec.Valid {
		return 0, fmt.Errorf("no historical data")
	}
	return avgReqPerSec.Float64, nil
}

func getHistoryPath() string {
	return "./exports/history.json"
}

func loadHistory() []string {
	data, err := os.ReadFile(getHistoryPath())
	if err != nil {
		return nil
	}
	var hist []string
	json.Unmarshal(data, &hist)
	return hist
}

func saveHistory(url string, history []string) []string {
	var newHist []string
	for _, h := range history {
		if h != url {
			newHist = append(newHist, h)
		}
	}
	newHist = append([]string{url}, newHist...)
	if len(newHist) > 20 {
		newHist = newHist[:20]
	}
	data, _ := json.MarshalIndent(newHist, "", "  ")
	os.WriteFile(getHistoryPath(), data, 0644)
	return newHist
}

func writeDebugLog(language string, msg string, stderr string) {
	os.MkdirAll("exports", 0755)
	logMsg := fmt.Sprintf("[%s] %s\n%s\n--- STDERR ---\n%s\n\n", time.Now().Format(time.RFC3339), language, msg, stderr)
	f, _ := os.OpenFile(filepath.Join("exports", "debug_ts.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if f != nil {
		f.WriteString(logMsg)
		f.Close()
	}
}

func initialModel() model {
	initDB()
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	columns := []table.Column{
		{Title: "Language", Width: 15},
		{Title: "Req/s", Width: 15},
		{Title: "Time", Width: 15},
		{Title: "Compare", Width: 15},
	}
	t := table.New(
		table.WithColumns(columns),
		table.WithFocused(true),
		table.WithHeight(7),
	)

	var inputs []textinput.Model = make([]textinput.Model, 3)
	inputs[0] = textinput.New()
	inputs[0].Placeholder = "http://localhost:8080/page/1"
	inputs[0].Focus()
	inputs[0].CharLimit = 100
	inputs[0].Width = 30
	inputs[0].Prompt = "URL: "

	inputs[1] = textinput.New()
	inputs[1].Placeholder = "10000"
	inputs[1].CharLimit = 10
	inputs[1].Width = 10
	inputs[1].Prompt = "Max URLs: "

	inputs[2] = textinput.New()
	inputs[2].Placeholder = "1"
	inputs[2].CharLimit = 5
	inputs[2].Width = 5
	inputs[2].Prompt = "Passes: "

	return model{
		state:        stateConfig,
		spinner:      s,
		crawlers:     []string{"Go", "TypeScript", "Rust"},
		table:        t,
		progressChan: make(chan progressMsg, 1000), // Buffered to handle rapid prints
		inputs:       inputs,
		focusIndex:   0,
		urlHistory:   loadHistory(),
		historyIndex: -1,
		showHelp:     false, // Hide help by default on startup
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

func runRealBenchmark(language string, targetURL string, maxURLs int, progChan chan<- progressMsg) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd

		maxFlag := fmt.Sprintf("--max=%d", maxURLs)
		urlFlag := fmt.Sprintf("--url=%s", targetURL)

		switch language {
		case "Go":
			cmd = exec.Command("./bin/crawler-go", urlFlag, maxFlag, "-c=100")
		case "TypeScript":
			cmd = exec.Command("node", "index.js", urlFlag, maxFlag, "-c=100")
			cmd.Dir = "./cmd/crawler-ts"
		case "Rust":
			cmd = exec.Command("../../bin/crawler-rust", urlFlag, maxFlag, "-c=100")
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
			writeDebugLog(language, fmt.Sprintf("START ERROR: %v\nCmd: %v\nDir: %v", err, cmd.Args, cmd.Dir), "")
			return benchmarkCompleteMsg{
				result: crawlerResult{Language: language + " (Start Err)", ReqPerSec: 0, TimeTaken: 0},
			}
		}

		// Collect all stderr lines for logging
		var stderrLines []string
		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				line := scanner.Text()
				stderrLines = append(stderrLines, line)
				if strings.HasPrefix(line, "PROGRESS: ") {
					trimmed := strings.TrimSpace(strings.TrimPrefix(line, "PROGRESS: "))
					parts := strings.SplitN(trimmed, "|", 2)

					valStr := strings.TrimSpace(parts[0])
					val, err := strconv.ParseInt(valStr, 10, 64)
					if err == nil {
						var recent []string
						if len(parts) == 2 {
							_ = json.Unmarshal([]byte(strings.TrimSpace(parts[1])), &recent)
						}

						select {
						case progChan <- progressMsg{reqs: val, recent: recent}:
						default:
							// Drop if too fast
						}
					}
				}
			}
		}()

		out, err := io.ReadAll(stdout)
		if err != nil {
			writeDebugLog(language, "IO ERROR reading stdout", strings.Join(stderrLines, "\n"))
			return benchmarkCompleteMsg{result: crawlerResult{Language: language + " (IO Err)"}}
		}

		cmd.Wait()

		// Always log for TypeScript to help diagnose issues
		if language == "TypeScript" {
			writeDebugLog(language, string(out), strings.Join(stderrLines, "\n"))
		}

		var res BenchmarkResult
		if err := json.Unmarshal(out, &res); err != nil {
			writeDebugLog(language, fmt.Sprintf("PARSE ERROR: %v\nRaw stdout: %s", err, string(out)), strings.Join(stderrLines, "\n"))
			return benchmarkCompleteMsg{
				result: crawlerResult{
					Language:  language + " (Parse Err)",
					ReqPerSec: 0,
					TimeTaken: 0,
				},
			}
		}

		// Ensure Language string exactly matches the expected Language string logic
		// if the original payload didn't already have it
		if res.Language == "" || len(res.Language) < 2 {
			res.Language = language
		}

		saveBenchmarkToDB(targetURL, res)

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
		case "up", "shift+tab":
			if m.state == stateConfig {
				m.focusIndex--
				if m.focusIndex < 0 {
					m.focusIndex = len(m.inputs) - 1
				}
				for i := 0; i < len(m.inputs); i++ {
					if i == m.focusIndex {
						m.inputs[i].Focus()
					} else {
						m.inputs[i].Blur()
					}
				}
				return m, nil
			}
		case "down", "tab":
			if m.state == stateConfig {
				m.focusIndex++
				if m.focusIndex >= len(m.inputs) {
					m.focusIndex = 0
				}
				for i := 0; i < len(m.inputs); i++ {
					if i == m.focusIndex {
						m.inputs[i].Focus()
					} else {
						m.inputs[i].Blur()
					}
				}
				return m, nil
			}
		case "shift+left":
			if m.state == stateConfig && m.focusIndex == 0 && len(m.urlHistory) > 0 {
				m.historyIndex++
				if m.historyIndex >= len(m.urlHistory) {
					m.historyIndex = len(m.urlHistory) - 1
				}
				m.inputs[0].SetValue(m.urlHistory[m.historyIndex])
				m.inputs[0].SetCursor(len(m.inputs[0].Value()))
				return m, nil
			}
		case "shift+right":
			if m.state == stateConfig && m.focusIndex == 0 {
				m.historyIndex--
				if m.historyIndex < 0 {
					m.historyIndex = -1
					m.inputs[0].SetValue("")
				} else if m.historyIndex < len(m.urlHistory) {
					m.inputs[0].SetValue(m.urlHistory[m.historyIndex])
					m.inputs[0].SetCursor(len(m.inputs[0].Value()))
				}
				return m, nil
			}
		case "enter":
			if m.state == stateConfig {
				targetURL := m.inputs[0].Value()
				if targetURL == "" {
					targetURL = "http://localhost:8080/page/1"
				}
				// Normalize: add https:// if no scheme provided (e.g. sitebulb.com -> https://sitebulb.com)
				if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
					targetURL = "https://" + targetURL
				}
				m.urlHistory = saveHistory(targetURL, m.urlHistory)
				m.historyIndex = -1
				maxURLs, err := strconv.Atoi(m.inputs[1].Value())
				if err != nil || maxURLs <= 0 {
					maxURLs = 10000
				}
				passes, err := strconv.Atoi(m.inputs[2].Value())
				if err != nil || passes <= 0 {
					passes = 1
				}

				m.targetURL = targetURL
				m.maxURLs = maxURLs
				m.totalPasses = passes
				m.state = stateRunning
				m.currentIdx = 0
				m.currentPass = 1
				m.currentReqs = 0
				m.recentURLs = nil
				m.results = nil // clear for new run
				return m, tea.Batch(
					runRealBenchmark(m.crawlers[m.currentIdx], m.targetURL, m.maxURLs, m.progressChan),
					waitForProgress(m.progressChan),
				)
			} else if m.state == stateResults {
				m.state = stateConfig
				return m, nil
			}
		case "?":
			if m.state == stateConfig && m.focusIndex != 0 {
				m.showHelp = !m.showHelp
				return m, nil
			} else if m.state == stateConfig && m.focusIndex == 0 {
				// We don't want '?' to toggle help when typing a URL since URLs can contain '?'
				// But we'll add an explicit check to make sure it functions normally as a text input
			}
		case "esc":
			if m.state == stateConfig && m.showHelp {
				m.showHelp = false
				return m, nil
			}
		case "r", "R":
			if m.state == stateResults {
				m.state = stateConfig
				return m, nil
			}
		}

	case progressMsg:
		m.currentReqs = msg.reqs
		m.recentURLs = msg.recent
		return m, waitForProgress(m.progressChan)

	case benchmarkCompleteMsg:
		m.results = append(m.results, msg.result)
		m.currentIdx++
		m.currentReqs = 0
		m.recentURLs = nil
		if m.currentIdx < len(m.crawlers) {
			return m, runRealBenchmark(m.crawlers[m.currentIdx], m.targetURL, m.maxURLs, m.progressChan)
		} else {
			m.currentPass++
			if m.currentPass <= m.totalPasses {
				m.currentIdx = 0
				return m, runRealBenchmark(m.crawlers[m.currentIdx], m.targetURL, m.maxURLs, m.progressChan)
			} else {
				m.state = stateResults
				m.updateTableData()
				return m, nil
			}
		}

	case spinner.TickMsg:
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	if m.state == stateConfig {
		for i := range m.inputs {
			m.inputs[i], cmd = m.inputs[i].Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *model) updateTableData() {
	rows := []table.Row{}

	// Aggregate results if passes > 1
	agg := make(map[string]struct {
		reqPerSec float64
		timeTaken time.Duration
		count     int
	})

	// Maintain order of languages
	for _, cr := range m.crawlers {
		agg[cr] = struct {
			reqPerSec float64
			timeTaken time.Duration
			count     int
		}{}
	}

	for _, r := range m.results {
		baseLang := r.Language
		// handle errors which might change the language field
		for _, cr := range m.crawlers {
			if strings.HasPrefix(r.Language, cr) {
				baseLang = cr
				break
			}
		}

		val := agg[baseLang]
		val.reqPerSec += r.ReqPerSec
		val.timeTaken += r.TimeTaken
		val.count++
		agg[baseLang] = val
	}

	for _, cr := range m.crawlers {
		val := agg[cr]
		if val.count > 0 {
			avgReq := val.reqPerSec / float64(val.count)
			avgTime := time.Duration(int64(val.timeTaken) / int64(val.count))

			compareStr := "N/A"
			histAvg, err := getHistoricalAverage(m.targetURL, cr)
			if err == nil && histAvg > 0 {
				delta := ((avgReq - histAvg) / histAvg) * 100
				if delta > 0 {
					compareStr = fmt.Sprintf("+%.2f%%", delta)
				} else if delta < 0 {
					compareStr = fmt.Sprintf("%.2f%%", delta)
				} else {
					compareStr = "0.00%"
				}
			}

			rows = append(rows, table.Row{
				cr,
				fmt.Sprintf("%.2f", avgReq),
				avgTime.String(),
				compareStr,
			})
		}
	}

	m.table.SetRows(rows)
	m.generateReport(rows)
}

func (m *model) generateReport(rows []table.Row) {
	timestamp := time.Now().Format("20060102_150405")
	filename := filepath.Join("exports", fmt.Sprintf("report_%s.md", timestamp))

	// Ensure directory exists
	os.MkdirAll("exports", 0755)

	var sb strings.Builder
	sb.WriteString("# Open-Crawl Benchmark Report\n\n")
	sb.WriteString(fmt.Sprintf("**Date:** %s\n", time.Now().Format(time.RFC1123)))
	sb.WriteString(fmt.Sprintf("**Target URL:** %s\n", m.targetURL))
	sb.WriteString(fmt.Sprintf("**Max URLs:** %d\n", m.maxURLs))
	sb.WriteString(fmt.Sprintf("**Passes:** %d\n\n", m.totalPasses))

	sb.WriteString("## Results\n\n")
	sb.WriteString("| Language | Req/s | Time | vs Historical Avg |\n")
	sb.WriteString("|----------|-------|------|-------------------|\n")

	bestLang := ""
	bestReqs := 0.0

	for _, row := range rows {
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n", row[0], row[1], row[2], row[3]))

		// Determine best performer (highest Req/s)
		if reqs, err := strconv.ParseFloat(row[1], 64); err == nil && reqs > bestReqs {
			bestReqs = reqs
			bestLang = row[0]
		}
	}

	sb.WriteString("\n## Summary\n\n")
	if bestLang != "" {
		sb.WriteString(fmt.Sprintf("The best performing crawler in this run was **%s** with **%.2f Req/s**.\n", bestLang, bestReqs))
	} else {
		sb.WriteString("No successful crawler results generated.\n")
	}

	err := os.WriteFile(filename, []byte(sb.String()), 0644)
	if err != nil {
		fmt.Printf("Error saving report: %v\n", err)
	}
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
		s += "Configure Benchmark and Press [ENTER] to start.\n\n"

		for i := range m.inputs {
			s += m.inputs[i].View()
			if i < len(m.inputs)-1 {
				s += "\n"
			}
		}
		if m.showHelp {
			helpBox := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("240")).
				Padding(1, 2).
				MarginTop(1)

			helpText := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Crawlers to run: Go, TypeScript, Rust\nConcurrency: 100") +
				"\n\n" +
				lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Render("Keybindings:") + "\n" +
				"  Tab/Shift+Tab : Switch inputs\n" +
				"  Up/Down       : Switch inputs\n" +
				"  Shift+Left    : Previous URL in history\n" +
				"  Shift+Right   : Next URL in history\n" +
				"  Enter         : Start benchmark\n" +
				"  ? or Esc      : Toggle this help menu\n" +
				"  Ctrl+C or q   : Quit"

			s += helpBox.Render(helpText)
		} else {
			s += "\n\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Press '?' to show help menu")
		}
	case stateRunning:
		bar := m.progressBar(int(m.currentReqs), m.maxURLs)
		s += fmt.Sprintf("%s Running %s crawler (Pass %d/%d)...\nTarget: %s\n\n%s %d/%d requests\n\n",
			m.spinner.View(), m.crawlers[m.currentIdx], m.currentPass, m.totalPasses, m.targetURL, bar, m.currentReqs, m.maxURLs)

		if len(m.recentURLs) > 0 {
			s += lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Recent URLs:") + "\n"
			for _, url := range m.recentURLs {
				s += "  " + url + "\n"
			}
		}
	case stateResults:
		s += "✅ Benchmarks Complete!\n\n"
		s += m.table.View()
		s += "\n\n📄 A markdown report has been saved to the ./exports folder!"
		s += "\n\nPress 'r' or 'Enter' to restart."
	}

	s += "\n\nPress 'q' or 'ctrl+c' to quit.\n"

	return lipgloss.NewStyle().Margin(1, 2).Render(s)
}
