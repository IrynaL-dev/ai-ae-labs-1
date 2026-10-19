package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
)

type nodeCtx struct {
	agent.StrictContextMock
}

func newNodeCtx() *nodeCtx {
	return &nodeCtx{StrictContextMock: agent.NewStrictContextMock(context.Background())}
}

type fakeConverter struct {
	markdown string
	err      error
}

func (f fakeConverter) ToMarkdown(context.Context, string) (string, error) {
	return f.markdown, f.err
}

const (
	fixtureMD     = "testdata/ledgerworks_soc2.md"
	fixturePDF    = "testdata/ledgerworks_soc2.pdf"
	fixtureDocMD  = "testdata/ledgerworks_soc2.docling.md"
	fixtureTables = 3
)

func TestLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	docx := filepath.Join(dir, "doc.docx")
	if err := os.WriteFile(docx, []byte("PK\x03\x04"), 0o600); err != nil {
		t.Fatalf("підготовка фікстури: %v", err)
	}

	tests := []struct {
		name     string
		path     string
		wantKind string
		wantErr  bool
	}{
		{name: "markdown", path: fixtureMD, wantKind: "markdown"},
		{name: "pdf за magic bytes", path: fixturePDF, wantKind: "pdf"},
		{name: "пробіли навколо шляху", path: "  " + fixtureMD + "\n", wantKind: "markdown"},
		{name: "файла немає", path: filepath.Join(dir, "ghost.md"), wantErr: true},
		{name: "порожній шлях", path: "", wantErr: true},
		{name: "непідтримуваний формат", path: docx, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := load(newNodeCtx(), tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("load(%q) = %+v, err = nil; очікували помилку", tt.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("load(%q) error = %v", tt.path, err)
			}
			if got.Kind != tt.wantKind {
				t.Errorf("load(%q).Kind = %q, очікували %q", tt.path, got.Kind, tt.wantKind)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		conv     fakeConverter
		src      Source
		contains string
		wantErr  string
	}{
		{name: "markdown читається як є", src: Source{Path: fixtureMD, Kind: "markdown"}, contains: "| F-101"},
		{name: "pdf іде в конвертер", conv: fakeConverter{markdown: "| a |\n|---|\n| 1 |"}, src: Source{Path: fixturePDF, Kind: "pdf"}, contains: "| 1 |"},
		{name: "помилка конвертера", conv: fakeConverter{err: errors.New("docling down")}, src: Source{Path: fixturePDF, Kind: "pdf"}, wantErr: "docling down"},
		{name: "порожній результат", conv: fakeConverter{markdown: "  \n"}, src: Source{Path: fixturePDF, Kind: "pdf"}, wantErr: "порожній"},
		{name: "markdown-файла немає", src: Source{Path: "testdata/ghost.md", Kind: "markdown"}, wantErr: "ghost.md"},
		{name: "невідомий тип", src: Source{Path: fixtureMD, Kind: "docx"}, wantErr: "невідомий тип"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := newParse(tt.conv)(newNodeCtx(), tt.src)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parse() err = %v, очікували %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse() error = %v", err)
			}
			if got.ID != "ledgerworks_soc2" {
				t.Errorf("Document.ID = %q, очікували ім'я файла без розширення", got.ID)
			}
			if !strings.Contains(got.Markdown, tt.contains) {
				t.Errorf("Markdown не містить %q", tt.contains)
			}
		})
	}
}

func TestDoclingFixtureKeepsTables(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(fixtureDocMD)
	if err != nil {
		t.Fatal(err)
	}
	md := string(data)
	if got := strings.Count(md, "\n|--"); got != fixtureTables {
		t.Errorf("таблиць у виході docling = %d, очікували %d", got, fixtureTables)
	}
	if strings.Contains(md, "\n### ") {
		t.Log("docling повернув ### — рівні заголовків збережено; перевірте свою логіку ієрархії")
	}
}

func TestChunk_Hierarchy(t *testing.T) {
	t.Parallel()
	got, err := chunk(newNodeCtx(), newDocument("d1", "", "# Розділ\n\nабзац\n"))
	if err != nil {
		t.Fatalf("chunk() error = %v", err)
	}
	if len(got.Chunks) < 2 {
		t.Fatalf("очікували ієрархічні чанки (parent та child), отримали %d", len(got.Chunks))
	}
	for _, c := range got.Chunks {
		if c.DocumentID != "d1" {
			t.Errorf("DocumentID = %q, очікували d1", c.DocumentID)
		}
	}
}

func TestChunk_EmptyDocumentIsError(t *testing.T) {
	t.Parallel()
	if _, err := chunk(newNodeCtx(), Document{}); err == nil {
		t.Fatal("chunk(порожній) = nil error; порожній документ має бути помилкою")
	}
}

func TestChunk_TableStaysWhole(t *testing.T) {
	data, err := os.ReadFile(fixtureDocMD)
	if err != nil {
		t.Fatal(err)
	}
	got, err := chunk(newNodeCtx(), newDocument("ledgerworks_soc2", fixtureDocMD, string(data)))
	if err != nil {
		t.Fatalf("chunk() error = %v", err)
	}

	var tables []Chunk
	for _, c := range got.Chunks {
		if c.Kind == "table" {
			tables = append(tables, c)
		}
	}
	if len(tables) != fixtureTables {
		t.Fatalf("чанків типу table = %d, очікували %d — таблицю розрізано або втрачено", len(tables), fixtureTables)
	}
	wantRows := map[string][]string{
		"Vendor":     {"Acme Bank JSC", "Northwind Cloud", "Globex Payments"},
		"Finding ID": {"F-101", "F-102", "F-103", "F-104", "F-105", "Severity"},
		"Merchant":   {"A-114", "A-207", "A-331", "NBU 2026 requirement"},
	}
	for header, rows := range wantRows {
		var whole bool
		for _, c := range tables {
			if !strings.HasPrefix(firstTableRow(c.Text), "| "+header) {
				continue
			}
			whole = true
			for _, row := range rows {
				if !strings.Contains(c.Text, row) {
					whole = false
					t.Errorf("у таблиці %q немає %q — рядок або заголовок втрачено:\n%s", header, row, c.Text)
				}
			}
			if c.ParentID == "" {
				t.Errorf("таблиця %q без ParentID — зв'язок із розділом втрачено", header)
			}
		}
		if !whole {
			t.Errorf("таблицю з заголовком %q не знайдено цілою", header)
		}
	}
}

func firstTableRow(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "|") {
			return line
		}
	}
	return ""
}

func TestStubNodesPassThrough(t *testing.T) {
	t.Parallel()
	in := Batch{Chunks: []Chunk{{ID: "c1", Text: "Acme Bank JSC"}}}
	st := NewStores()
	nodes := map[string]func(agent.Context, Batch) (Batch, error){
		"extract_entities": extractEntities,
		"index_vector":     newIndexVector(st.Vector),
		"index_graph":      newIndexGraph(st.Graph),
	}
	for name, node := range nodes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := node(newNodeCtx(), in)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(got.Chunks) != len(in.Chunks) {
				t.Errorf("%s загубив чанки: %d → %d", name, len(in.Chunks), len(got.Chunks))
			}
		})
	}
}

func TestReport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		chunks   []Chunk
		contains string
	}{
		{name: "порожній вхід", chunks: nil, contains: "0"},
		{name: "один чанк", chunks: []Chunk{{ID: "c1"}}, contains: "1"},
		{name: "три чанки", chunks: []Chunk{{ID: "c1"}, {ID: "c2"}, {ID: "c3"}}, contains: "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := report(newNodeCtx(), Batch{Chunks: tt.chunks})
			if err != nil {
				t.Fatalf("report() error = %v", err)
			}
			if !strings.Contains(got, tt.contains) {
				t.Errorf("report() = %q; очікували підрядок %q", got, tt.contains)
			}
		})
	}
}

func TestGraph_EarlyWin(t *testing.T) {
	t.Parallel()
	a, err := newGraph(fakeConverter{}, NewStores())
	if err != nil {
		t.Fatalf("newGraph: %v", err)
	}
	res, err := labrun.Run(context.Background(), a, fixtureMD)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Final, "Ingestion Pipeline Report") {
		t.Errorf("фінальна відповідь = %q; очікували звіт пайплайна", res.Final)
	}
	if got := len(res.Events); got != 7 {
		t.Errorf("подій = %d, очікували 7 (load … report)", got)
	}
}

func TestChunkJSONContract(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(Chunk{ID: "c", DocumentID: "d", ParentID: "p", Level: 2, Kind: "table", SectionPath: "s", Text: "t"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"id"`, `"document_id"`, `"parent_id"`, `"level"`, `"kind"`, `"section_path"`, `"text"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("JSON чанка без поля %s: %s", key, raw)
		}
	}
}
