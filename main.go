// granola-sync: Export Granola meetings to local Markdown files.
//
// Usage:
//
//	GRANOLA_API_KEY=<key> ./granola-sync [--since YYYY-MM-DD]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const baseURL = "https://public-api.granola.ai/v1"

// ── API types ──────────────────────────────────────────────────────────────────

type NoteSummary struct {
	ID        string `json:"id"`
	Title     *string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type ListNotesResponse struct {
	Notes   []NoteSummary `json:"notes"`
	HasMore bool          `json:"hasMore"`
	Cursor  *string       `json:"cursor"`
}

type User struct {
	Name  *string `json:"name"`
	Email string  `json:"email"`
}

type CalendarEvent struct {
	EventTitle         *string  `json:"event_title"`
	Organiser          *string  `json:"organiser"`
	ScheduledStartTime *string  `json:"scheduled_start_time"`
	ScheduledEndTime   *string  `json:"scheduled_end_time"`
}

type Folder struct {
	Name *string `json:"name"`
}

type Speaker struct {
	Source string `json:"source"` // "microphone" or "speaker"
}

type TranscriptSegment struct {
	Speaker   Speaker `json:"speaker"`
	Text      string  `json:"text"`
	StartTime *string `json:"start_time"`
	EndTime   *string `json:"end_time"`
}

type Note struct {
	ID              string              `json:"id"`
	Title           *string             `json:"title"`
	CreatedAt       string              `json:"created_at"`
	Attendees       []User              `json:"attendees"`
	CalendarEvent   *CalendarEvent      `json:"calendar_event"`
	FolderMembership []Folder           `json:"folder_membership"`
	SummaryText     string              `json:"summary_text"`
	SummaryMarkdown *string             `json:"summary_markdown"`
	Transcript      []TranscriptSegment `json:"transcript"`
}

// ── API client ─────────────────────────────────────────────────────────────────

type client struct {
	apiKey string
	http   *http.Client
}

func newClient(apiKey string) *client {
	return &client{apiKey: apiKey, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *client) get(path string, params url.Values, out any) error {
	req, err := http.NewRequest("GET", baseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, path)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *client) listAllNotes(since string) ([]NoteSummary, error) {
	var all []NoteSummary
	params := url.Values{"page_size": {"30"}}
	if since != "" {
		params.Set("created_after", since)
	}

	for {
		var resp ListNotesResponse
		if err := c.get("/notes", params, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Notes...)
		fmt.Printf("  fetched %d notes (total so far: %d)\n", len(resp.Notes), len(all))

		if !resp.HasMore || resp.Cursor == nil {
			break
		}
		params.Set("cursor", *resp.Cursor)
	}
	return all, nil
}

func (c *client) getNote(id string) (*Note, error) {
	var note Note
	params := url.Values{"include": {"transcript"}}
	if err := c.get("/notes/"+id, params, &note); err != nil {
		return nil, err
	}
	return &note, nil
}

// ── Markdown rendering ─────────────────────────────────────────────────────────

func noteToMarkdown(n *Note) string {
	var b strings.Builder

	title := deref(n.Title, "Untitled")
	fmt.Fprintf(&b, "# %s\n\n", title)

	// Prefer scheduled meeting time from calendar; fall back to created_at
	cal := n.CalendarEvent
	if cal != nil && cal.ScheduledStartTime != nil {
		start, errS := time.Parse(time.RFC3339, *cal.ScheduledStartTime)
		if errS == nil {
			line := fmt.Sprintf("**Date:** %s", start.UTC().Format("2006-01-02 15:04 UTC"))
			if cal.ScheduledEndTime != nil {
				if end, err := time.Parse(time.RFC3339, *cal.ScheduledEndTime); err == nil {
					dur := end.Sub(start).Round(time.Minute)
					line += fmt.Sprintf(" (%d min)", int(dur.Minutes()))
				}
			}
			fmt.Fprintln(&b, line)
		}
	} else if n.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, n.CreatedAt); err == nil {
			fmt.Fprintf(&b, "**Date:** %s\n", t.UTC().Format("2006-01-02 15:04 UTC"))
		}
	}

	if cal != nil {
		if calTitle := deref(cal.EventTitle, ""); calTitle != "" && calTitle != title {
			fmt.Fprintf(&b, "**Calendar event:** %s\n", calTitle)
		}
		if org := deref(cal.Organiser, ""); org != "" {
			fmt.Fprintf(&b, "**Organiser:** %s\n", org)
		}
	}

	if len(n.Attendees) > 0 {
		parts := make([]string, 0, len(n.Attendees))
		for _, a := range n.Attendees {
			name := deref(a.Name, "")
			if name != "" && a.Email != "" {
				parts = append(parts, fmt.Sprintf("%s (%s)", name, a.Email))
			} else if a.Email != "" {
				parts = append(parts, a.Email)
			} else if name != "" {
				parts = append(parts, name)
			}
		}
		fmt.Fprintf(&b, "**Attendees:** %s\n", strings.Join(parts, ", "))
	}

	if len(n.FolderMembership) > 0 {
		names := make([]string, 0)
		for _, f := range n.FolderMembership {
			if name := deref(f.Name, ""); name != "" {
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "**Folders:** %s\n", strings.Join(names, ", "))
		}
	}

	b.WriteString("\n")

	// Summary
	summary := ""
	if n.SummaryMarkdown != nil && *n.SummaryMarkdown != "" {
		summary = *n.SummaryMarkdown
	} else if n.SummaryText != "" {
		summary = n.SummaryText
	}
	if summary != "" {
		fmt.Fprintf(&b, "## Summary\n\n%s\n\n", strings.TrimSpace(summary))
	}

	// Transcript
	if len(n.Transcript) > 0 {
		b.WriteString("## Transcript\n\n")
		// Resolve meeting start to compute relative timestamps
		var meetingStart *time.Time
		if cal != nil && cal.ScheduledStartTime != nil {
			if t, err := time.Parse(time.RFC3339, *cal.ScheduledStartTime); err == nil {
				meetingStart = &t
			}
		}

		for _, seg := range n.Transcript {
			label := speakerLabel(seg.Speaker.Source)
			ts := ""
			if seg.StartTime != nil {
				if t, err := time.Parse(time.RFC3339, *seg.StartTime); err == nil {
					var offset time.Duration
					if meetingStart != nil {
						offset = t.Sub(*meetingStart).Round(time.Second)
					} else {
						offset = t.Sub(t.Truncate(24 * time.Hour))
					}
					if offset < 0 {
						offset = 0
					}
					m := int(offset.Minutes())
					s := int(offset.Seconds()) % 60
					ts = fmt.Sprintf("[%02d:%02d] ", m, s)
				}
			}
			fmt.Fprintf(&b, "**%s:** %s%s\n", label, ts, strings.TrimSpace(seg.Text))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// speakerLabel maps transcript source to a readable label.
func speakerLabel(source string) string {
	switch source {
	case "microphone":
		return "You"
	case "speaker":
		return "Them"
	default:
		return source
	}
}

// ── File naming ────────────────────────────────────────────────────────────────

var unsafeChars = regexp.MustCompile(`[^\w\s-]`)
var spaces = regexp.MustCompile(`[\s_]+`)

func sanitize(s string) string {
	s = unsafeChars.ReplaceAllString(s, "")
	s = spaces.ReplaceAllString(strings.TrimSpace(s), "_")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// noteDate returns the meeting date, preferring scheduled_start_time over created_at.
func noteDate(n *Note) time.Time {
	if n.CalendarEvent != nil && n.CalendarEvent.ScheduledStartTime != nil {
		if t, err := time.Parse(time.RFC3339, *n.CalendarEvent.ScheduledStartTime); err == nil {
			return t.UTC()
		}
	}
	if n.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, n.CreatedAt); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// noteSubdir returns the relative subdirectory: FolderName/YYYY/MM/
func noteSubdir(n *Note) string {
	folder := "Unclassified"
	if len(n.FolderMembership) > 0 {
		if name := deref(n.FolderMembership[0].Name, ""); name != "" {
			folder = sanitize(name)
		}
	}
	d := noteDate(n)
	if d.IsZero() {
		return filepath.Join(folder, "0000", "00")
	}
	return filepath.Join(folder, d.Format("2006"), d.Format("01"))
}

// noteRelPath returns the full relative path (subdir + filename) for a note.
func noteRelPath(n *Note) string {
	d := noteDate(n)
	dateStr := "0000-00-00"
	if !d.IsZero() {
		dateStr = d.Format("2006-01-02")
	}
	title := deref(n.Title, "untitled")
	filename := fmt.Sprintf("%s_%s.md", dateStr, sanitize(title))
	return filepath.Join(noteSubdir(n), filename)
}

// ── Index (deduplication) ──────────────────────────────────────────────────────

type IndexEntry struct {
	UpdatedAt   string `json:"updated_at"`
	Path        string `json:"path"`          // relative path (human reference)
	DriveFileID string `json:"drive_file_id"` // Drive ID for deletion/update
}

type driveIndex map[string]IndexEntry // note_id → entry

// ── Config ─────────────────────────────────────────────────────────────────────

type config struct {
	granolaAPIKey string
	clientID      string
	clientSecret  string
	refreshToken  string
	driveFolderID string
	since         string
}

func loadConfig(since string) (config, error) {
	cfg := config{
		granolaAPIKey: os.Getenv("GRANOLA_API_KEY"),
		clientID:      os.Getenv("GOOGLE_CLIENT_ID"),
		clientSecret:  os.Getenv("GOOGLE_CLIENT_SECRET"),
		refreshToken:  os.Getenv("GOOGLE_REFRESH_TOKEN"),
		driveFolderID: os.Getenv("GOOGLE_DRIVE_FOLDER_ID"),
		since:         since,
	}
	if cfg.granolaAPIKey == "" {
		return cfg, fmt.Errorf("GRANOLA_API_KEY is not set")
	}
	if cfg.clientID == "" {
		return cfg, fmt.Errorf("GOOGLE_CLIENT_ID is not set")
	}
	if cfg.clientSecret == "" {
		return cfg, fmt.Errorf("GOOGLE_CLIENT_SECRET is not set")
	}
	if cfg.refreshToken == "" {
		return cfg, fmt.Errorf("GOOGLE_REFRESH_TOKEN is not set (run: ./granola-sync auth)")
	}
	if cfg.driveFolderID == "" {
		return cfg, fmt.Errorf("GOOGLE_DRIVE_FOLDER_ID is not set")
	}
	return cfg, nil
}

// ── Main sync ──────────────────────────────────────────────────────────────────

func sync(cfg config) error {
	ctx := context.Background()

	dw, err := newDriveWriter(ctx, cfg.clientID, cfg.clientSecret, cfg.refreshToken, cfg.driveFolderID)
	if err != nil {
		return fmt.Errorf("init drive: %w", err)
	}

	idx, indexFileID, err := dw.LoadIndex()
	if err != nil {
		return fmt.Errorf("load index: %w", err)
	}
	fmt.Printf("Index loaded: %d known notes.\n", len(idx))

	c := newClient(cfg.granolaAPIKey)

	fmt.Println("Fetching note list...")
	summaries, err := c.listAllNotes(cfg.since)
	if err != nil {
		return fmt.Errorf("list notes: %w", err)
	}
	fmt.Printf("Total notes found: %d\n\n", len(summaries))

	var newCount, updatedCount, skippedCount int

	for _, s := range summaries {
		entry, known := idx[s.ID]
		if known && entry.UpdatedAt == s.UpdatedAt {
			skippedCount++
			continue
		}

		title := deref(s.Title, s.ID)
		if known {
			updatedCount++
			fmt.Printf("  [update] %s\n", title)
			if entry.DriveFileID != "" {
				if err := dw.DeleteFile(entry.DriveFileID); err != nil {
					fmt.Printf("  [warn]   delete old file: %v\n", err)
				}
			}
		} else {
			newCount++
			fmt.Printf("  [new]    %s\n", title)
		}

		note, err := c.getNote(s.ID)
		if err != nil {
			fmt.Printf("  [error]  %s: %v\n", s.ID, err)
			continue
		}

		relPath := noteRelPath(note)
		md := noteToMarkdown(note)

		fileID, err := dw.WriteFile(relPath, []byte(md))
		if err != nil {
			fmt.Printf("  [error]  upload %s: %v\n", relPath, err)
			continue
		}

		idx[s.ID] = IndexEntry{
			UpdatedAt:   s.UpdatedAt,
			Path:        relPath,
			DriveFileID: fileID,
		}
	}

	if err := dw.SaveIndex(idx, indexFileID); err != nil {
		return fmt.Errorf("save index: %w", err)
	}

	fmt.Printf("\nDone. new=%d updated=%d skipped=%d\n", newCount, updatedCount, skippedCount)
	return nil
}

func main() {
	// auth subcommand — run once locally to get a refresh token
	if len(os.Args) > 1 && os.Args[1] == "auth" {
		clientID     := os.Getenv("GOOGLE_CLIENT_ID")
		clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
		if clientID == "" || clientSecret == "" {
			log.Fatal("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET must be set")
		}
		RunAuth(clientID, clientSecret)
		return
	}

	since    := flag.String("since", "", "Only fetch notes created after this date (YYYY-MM-DD)")
	interval := flag.Duration("interval", 0, "Re-sync interval (e.g. 1h, 30m). If 0, run once and exit.")
	flag.Parse()

	// Also allow interval via env so it can be set in .env / docker-compose
	if *interval == 0 {
		if v := os.Getenv("SYNC_INTERVAL"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				log.Fatalf("invalid SYNC_INTERVAL %q: %v", v, err)
			}
			*interval = d
		}
	}

	cfg, err := loadConfig(*since)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	run := func() {
		log.Println("Starting sync...")
		if err := sync(cfg); err != nil {
			log.Printf("sync error: %v", err)
		}
	}

	run()

	if *interval > 0 {
		ticker := time.NewTicker(*interval)
		defer ticker.Stop()
		for range ticker.C {
			run()
		}
	}
}


// ── Helpers ────────────────────────────────────────────────────────────────────

func deref(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}
