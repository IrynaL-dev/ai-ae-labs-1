package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/corpus"
	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/embed"
)

// newGraph збирає agentic retrieval-конвеєр із fan-out + JoinNode + RRF:
//
//	Start → cache_check ─┬─ vector_search  ─┐
//	                     └─ keyword_search ─┴─► join → merge_rrf → rerank → answer
func newGraph(r *retriever) (agent.Agent, error) {
	cfg := workflow.NodeConfig{}

	cacheCheck := workflow.NewFunctionNode("cache_check", r.cacheCheck, cfg)
	vectorSearch := workflow.NewFunctionNode("vector_search", r.vectorSearch, cfg)
	keywordSearch := workflow.NewFunctionNode("keyword_search", r.keywordSearch, cfg)
	joinNode := workflow.NewJoinNode("join")
	mergeRRF := workflow.NewFunctionNode("merge_rrf", r.mergeRRF, cfg)
	rerankNode := workflow.NewFunctionNode("rerank", r.rerank, cfg)
	answerNode := workflow.NewFunctionNode("answer", r.answer, cfg)

	edges := workflow.NewEdgeBuilder().
		Add(workflow.Start, cacheCheck).
		AddFanOut(cacheCheck, vectorSearch, keywordSearch).
		AddFanIn(joinNode, vectorSearch, keywordSearch).
		Add(joinNode, mergeRRF).
		Add(mergeRRF, rerankNode).
		Add(rerankNode, answerNode).
		Build()

	return workflowagent.New(workflowagent.Config{
		Name:        "high_precision_rag",
		Description: "Agentic RAG: семантичний кеш → fan-out (vector + keyword) → JoinNode + RRF → re-rank → відповідь із provenance.",
		Edges:       edges,
	})
}

func (r *retriever) cacheCheck(ctx agent.Context, query string) (SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return SearchResult{}, fmt.Errorf("порожній запит")
	}
	if answer, ok := r.cache.get(ctx, r.emb, query, "default", "v1"); ok {
		return SearchResult{
			Query:      query,
			CacheHit:   true,
			Candidates: []Candidate{{ChunkID: "cache", Text: answer, Score: 1}},
		}, nil
	}
	return SearchResult{Query: query}, nil
}

func (r *retriever) vectorSearch(ctx agent.Context, in SearchResult) (SearchResult, error) {
	if in.CacheHit {
		return in, nil
	}
	var valid []corpus.Chunk
	texts := []string{in.Query}
	for _, c := range r.chunks {
		if c.Kind == "heading" {
			continue
		}
		valid = append(valid, c)
		texts = append(texts, c.Text)
	}
	vecs, err := r.emb.Embed(ctx, texts)
	if err != nil {
		return SearchResult{}, err
	}
	qVec := vecs[0]
	var out []Candidate
	for i, c := range valid {
		if cos := embed.Cosine(qVec, vecs[i+1]); cos >= 0.25 {
			out = append(out, Candidate{
				ChunkID:     c.ID,
				DocumentID:  c.DocumentID,
				SectionPath: c.SectionPath,
				Text:        c.Text,
				Score:       cos,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > topK {
		out = out[:topK]
	}
	return SearchResult{Query: in.Query, Candidates: out}, nil
}

func (r *retriever) keywordSearch(_ agent.Context, in SearchResult) (SearchResult, error) {
	if in.CacheHit {
		return in, nil
	}
	q := tokens(in.Query)
	var out []Candidate
	for _, c := range r.chunks {
		if c.Kind == "heading" {
			continue
		}
		if s := overlap(q, tokens(c.Text)); s > 0 {
			out = append(out, Candidate{
				ChunkID:     c.ID,
				DocumentID:  c.DocumentID,
				SectionPath: c.SectionPath,
				Text:        c.Text,
				Score:       s,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > topK {
		out = out[:topK]
	}
	return SearchResult{Query: in.Query, Candidates: out}, nil
}

func (r *retriever) mergeRRF(_ agent.Context, in map[string]any) (SearchResult, error) {
	vecRes := toSearchResult(in["vector_search"])
	kwRes := toSearchResult(in["keyword_search"])

	if vecRes.CacheHit {
		return vecRes, nil
	}
	if kwRes.CacheHit {
		return kwRes, nil
	}

	query := vecRes.Query
	if query == "" {
		query = kwRes.Query
	}

	const rrfK = 60.0
	scores := map[string]float64{}
	byID := map[string]Candidate{}

	for _, list := range [][]Candidate{vecRes.Candidates, kwRes.Candidates} {
		for rank, c := range list {
			scores[c.ChunkID] += 1.0 / (rrfK + float64(rank+1))
			if _, ok := byID[c.ChunkID]; !ok {
				byID[c.ChunkID] = c
			}
		}
	}

	out := make([]Candidate, 0, len(byID))
	for id, c := range byID {
		c.Score = scores[id]
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > topK {
		out = out[:topK]
	}
	return SearchResult{Query: query, Candidates: out}, nil
}

func toSearchResult(v any) SearchResult {
	if sr, ok := v.(SearchResult); ok {
		return sr
	}
	b, err := json.Marshal(v)
	if err != nil {
		return SearchResult{}
	}
	var sr SearchResult
	_ = json.Unmarshal(b, &sr)
	return sr
}
