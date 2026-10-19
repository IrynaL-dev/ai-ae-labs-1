package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/chunkeval"
	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/kgraph"
	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/mdblocks"
	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/vecindex"
)

type Source struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type Document struct {
	ID       string           `json:"id"`
	Path     string           `json:"path"`
	Markdown string           `json:"markdown"`
	Blocks   []mdblocks.Block `json:"blocks"`
}

type Chunk struct {
	ID          string `json:"id"`
	DocumentID  string `json:"document_id"`
	ParentID    string `json:"parent_id"`
	Level       int    `json:"level"`
	Kind        string `json:"kind"`
	SectionPath string `json:"section_path"`
	Text        string `json:"text"`
}

type Entity struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	SourceChunkID string `json:"source_chunk_id"`
}

type Batch struct {
	Document     Document `json:"document"`
	Chunks       []Chunk  `json:"chunks"`
	Entities     []Entity `json:"entities,omitempty"`
	VectorsAdded int      `json:"vectors_added"`
	NodesAdded   int      `json:"nodes_added"`
}

type MarkdownConverter interface {
	ToMarkdown(ctx context.Context, path string) (string, error)
}

type Stores struct {
	Vector *vecindex.Index
	Graph  *kgraph.Graph
}

func NewStores() Stores {
	return Stores{Vector: vecindex.New(), Graph: kgraph.New()}
}

func load(_ agent.Context, path string) (Source, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Source{}, fmt.Errorf("порожній шлях: введіть шлях до PDF або Markdown")
	}
	f, err := os.Open(path)
	if err != nil {
		return Source{}, fmt.Errorf("не вдалося прочитати %q: %w", path, err)
	}
	defer f.Close()
	head := make([]byte, 5)
	n, _ := f.Read(head)
	if bytes.HasPrefix(head[:n], []byte("%PDF-")) {
		return Source{Path: path, Kind: "pdf"}, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".txt":
		return Source{Path: path, Kind: "markdown"}, nil
	}
	return Source{}, fmt.Errorf("%q: непідтримуваний формат — очікуємо PDF або Markdown", path)
}

func newParse(conv MarkdownConverter) func(agent.Context, Source) (Document, error) {
	return func(ctx agent.Context, src Source) (Document, error) {
		var md string
		switch src.Kind {
		case "pdf":
			out, err := conv.ToMarkdown(ctx, src.Path)
			if err != nil {
				return Document{}, fmt.Errorf("parse %q: %w", src.Path, err)
			}
			md = out
		case "markdown":
			data, err := os.ReadFile(src.Path)
			if err != nil {
				return Document{}, fmt.Errorf("parse %q: %w", src.Path, err)
			}
			md = string(data)
		default:
			return Document{}, fmt.Errorf("parse: невідомий тип %q", src.Kind)
		}
		if strings.TrimSpace(md) == "" {
			return Document{}, fmt.Errorf("parse %q: порожній документ", src.Path)
		}
		return newDocument(documentID(src.Path), src.Path, md), nil
	}
}

func newDocument(id, path, md string) Document {
	return Document{ID: id, Path: path, Markdown: md, Blocks: mdblocks.Parse(md)}
}

func documentID(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

var reHeadingNum = regexp.MustCompile(`^(\d+(?:\.\d+)*)`)

func getTrueHeadingDepth(text string, blockDepth int) int {
	match := reHeadingNum.FindString(strings.TrimSpace(text))
	if match != "" {
		return strings.Count(match, ".") + 1
	}
	if blockDepth > 0 {
		return blockDepth
	}
	return 1
}

func chunk(_ agent.Context, doc Document) (Batch, error) {
	if strings.TrimSpace(doc.Markdown) == "" {
		return Batch{}, fmt.Errorf("порожній документ")
	}

	var chunks []Chunk
	var currentParentID string
	var currentParentIndex = 0
	var sectionPathParts []string

	createParent := func(title string) string {
		currentParentIndex++
		pID := fmt.Sprintf("%s-parent-%d", doc.ID, currentParentIndex)
		chunks = append(chunks, Chunk{
			ID:          pID,
			DocumentID:  doc.ID,
			ParentID:    "",
			Level:       0,
			Kind:        "heading",
			SectionPath: strings.Join(sectionPathParts, " / "),
			Text:        title,
		})
		return pID
	}

	currentParentID = createParent(doc.ID)

	childIndex := 0
	for _, block := range doc.Blocks {
		switch block.Kind {
		case mdblocks.KindHeading:
			depth := getTrueHeadingDepth(block.Text, block.Depth)
			title := strings.TrimSpace(block.Text)

			if depth <= 2 {
				sectionPathParts = []string{title}
				currentParentID = createParent(title)
			} else {
				if len(sectionPathParts) >= depth-1 {
					sectionPathParts = sectionPathParts[:depth-2]
				}
				sectionPathParts = append(sectionPathParts, title)
			}

			childIndex++
			cID := fmt.Sprintf("%s-child-%d", doc.ID, childIndex)
			chunks = append(chunks, Chunk{
				ID:          cID,
				DocumentID:  doc.ID,
				ParentID:    currentParentID,
				Level:       1,
				Kind:        "heading",
				SectionPath: strings.Join(sectionPathParts, " / "),
				Text:        block.Text,
			})

		case mdblocks.KindTable:
			childIndex++
			cID := fmt.Sprintf("%s-child-%d", doc.ID, childIndex)
			chunks = append(chunks, Chunk{
				ID:          cID,
				DocumentID:  doc.ID,
				ParentID:    currentParentID,
				Level:       2,
				Kind:        "table",
				SectionPath: strings.Join(sectionPathParts, " / "),
				Text:        block.Text,
			})

		default:
			childIndex++
			cID := fmt.Sprintf("%s-child-%d", doc.ID, childIndex)
			
			text := block.Text
			if chunkeval.Tokens(text) > maxChildTokens {
				sentences := strings.Split(text, ". ")
				var buf strings.Builder
				for _, s := range sentences {
					if buf.Len() > 0 && chunkeval.Tokens(buf.String()+". "+s) > maxChildTokens {
						chunks = append(chunks, Chunk{
							ID:          cID,
							DocumentID:  doc.ID,
							ParentID:    currentParentID,
							Level:       2,
							Kind:        string(block.Kind),
							SectionPath: strings.Join(sectionPathParts, " / "),
							Text:        strings.TrimSpace(buf.String()),
						})
						childIndex++
						cID = fmt.Sprintf("%s-child-%d", doc.ID, childIndex)
						buf.Reset()
					}
					if buf.Len() > 0 {
						buf.WriteString(". ")
					}
					buf.WriteString(s)
				}
				if buf.Len() > 0 {
					chunks = append(chunks, Chunk{
						ID:          cID,
						DocumentID:  doc.ID,
						ParentID:    currentParentID,
						Level:       2,
						Kind:        string(block.Kind),
						SectionPath: strings.Join(sectionPathParts, " / "),
						Text:        strings.TrimSpace(buf.String()),
					})
				}
			} else {
				chunks = append(chunks, Chunk{
					ID:          cID,
					DocumentID:  doc.ID,
					ParentID:    currentParentID,
					Level:       2,
					Kind:        string(block.Kind),
					SectionPath: strings.Join(sectionPathParts, " / "),
					Text:        text,
				})
			}
		}
	}

	return Batch{Document: doc, Chunks: chunks}, nil
}

const maxChildTokens = 200

func extractEntities(_ agent.Context, b Batch) (Batch, error) {
	var entities []Entity

	for _, c := range b.Chunks {
		if c.Kind == "table" {
			parsedBlocks := mdblocks.Parse(c.Text)
			for _, pb := range parsedBlocks {
				if pb.Kind == mdblocks.KindTable && pb.Table != nil {
					vendors := pb.Table.Column("Vendor")
					findings := pb.Table.Column("Finding ID")
					severities := pb.Table.Column("Severity")

					for i := range vendors {
						vName := strings.TrimSpace(vendors[i])
						if vName != "" && vName != "Vendor" && vName != "---" {
							entID := fmt.Sprintf("vendor:%s", strings.ToLower(strings.ReplaceAll(vName, " ", "-")))
							entities = append(entities, Entity{
								ID:            entID,
								Type:          "Vendor",
								Name:          vName,
								SourceChunkID: c.ID,
							})
						}
					}

					for i := range findings {
						fName := strings.TrimSpace(findings[i])
						if fName != "" && fName != "Finding ID" && fName != "---" {
							entID := fmt.Sprintf("finding:%s", strings.ToLower(strings.ReplaceAll(fName, " ", "-")))
							entities = append(entities, Entity{
								ID:            entID,
								Type:          "Finding",
								Name:          fName,
								SourceChunkID: c.ID,
							})
						}
					}

					for i := range severities {
						sName := strings.TrimSpace(severities[i])
						if sName == "High" || sName == "Critical" {
							entID := fmt.Sprintf("severity:%s", strings.ToLower(sName))
							entities = append(entities, Entity{
								ID:            entID,
								Type:          "Severity",
								Name:          sName,
								SourceChunkID: c.ID,
							})
						}
					}
				}
			}
		}
	}

	b.Entities = entities
	return b, nil
}

func newIndexVector(idx *vecindex.Index) func(agent.Context, Batch) (Batch, error) {
	return func(_ agent.Context, b Batch) (Batch, error) {
		added := 0
		for _, c := range b.Chunks {
			if c.ParentID != "" {
				idx.Add(c.ID, c.Text)
				added++
			}
		}
		b.VectorsAdded = added
		return b, nil
	}
}

func newIndexGraph(g *kgraph.Graph) func(agent.Context, Batch) (Batch, error) {
    return func(_ agent.Context, b Batch) (Batch, error) {
        nodesAdded := 0
        for _, e := range b.Entities {
            added, _ := g.AddNode(kgraph.Node{
                ID:      e.ID,
                Type:    e.Type,
                Name:    e.Name,
                Sources: []string{e.SourceChunkID},
            })
            if added {
                nodesAdded++
            }
        }
        b.NodesAdded = nodesAdded
        return b, nil
    }
}

func report(_ agent.Context, b Batch) (string, error) {
	var chunkTexts []string
	for _, c := range b.Chunks {
		chunkTexts = append(chunkTexts, c.Text)
	}

	m := chunkeval.Measure(chunkTexts)
	docTables := mdblocks.Tables(b.Document.Blocks)
	
	var tableTexts []string
	for _, dt := range docTables {
		tableTexts = append(tableTexts, dt.Text)
	}

	preserved := chunkeval.Preserved(chunkTexts, tableTexts)

	parentsCount := 0
	childrenCount := 0
	for _, c := range b.Chunks {
		if c.ParentID == "" {
			parentsCount++
		} else {
			childrenCount++
		}
	}

	tablesCount := 0
	headingsCount := 0
	textCount := 0
	listsCount := 0
	for _, block := range b.Document.Blocks {
		switch block.Kind {
		case mdblocks.KindTable:
			tablesCount++
		case mdblocks.KindHeading:
			headingsCount++
		case mdblocks.KindText:
			textCount++
		case mdblocks.KindList:
			listsCount++
		}
	}

	return fmt.Sprintf(
		"=== Ingestion Pipeline Report ===\n"+
			"Document: %s\n"+
			"Blocks:   %d total (%d tables, %d headings, %d text blocks)\n"+
			"Chunks:   %d total (%d parents, %d children)\n"+
			"Tokens:   min=%d, avg=%0.1f, max=%d\n"+
			"Tables:   %d/%d preserved (Lossless)\n"+
			"Entities: %d extracted, Vectors added: %d, Graph nodes added: %d",
		b.Document.ID,
		len(b.Document.Blocks),
		tablesCount,
		headingsCount,
		textCount,
		len(b.Chunks),
		parentsCount,
		childrenCount,
		m.Min,
		m.Avg,
		m.Max,
		preserved,
		len(docTables),
		len(b.Entities),
		b.VectorsAdded,
		b.NodesAdded,
	), nil
}
