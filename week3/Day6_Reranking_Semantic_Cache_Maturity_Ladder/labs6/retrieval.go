package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/corpus"
	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/embed"
)

// Candidate — кандидат retrieval з оцінкою і provenance.
type Candidate struct {
	ChunkID     string  `json:"chunk_id"`
	DocumentID  string  `json:"document_id"`
	SectionPath string  `json:"section_path,omitempty"`
	Text        string  `json:"text"`
	Score       float64 `json:"score"`
}

// SearchResult — запит + кандидати, тече між вузлами графа.
type SearchResult struct {
	Query      string      `json:"query"`
	Candidates []Candidate `json:"candidates"`
	CacheHit   bool        `json:"cache_hit"`
}

// topK — скільки кандидатів search віддає в rerank.
const topK = 5

// answerCache — кеш відповідей. Безпечний для одночасних викликів: коли ви
// зробите fan-out, вузли працюватимуть паралельно, а launcher у режимі web
// обслуговує кілька запитів одночасно.
//
// TODO(студент): семантичний кеш. Зараз ключ — нормалізований рядок запиту
// (точний hit). Перейдіть на схожість: r.emb.Embed + embed.Cosine проти
// збережених векторів запитів — перефразований запит має влучати, а запит
// про T-1 не має отримати відповідь про T-2. Ключ має ізолювати tenant, версію
// корпусу й TTL (Homework п.4).
const cacheTTL = 10 * time.Minute
const cacheSimThreshold = 0.45

type cacheEntry struct {
	query     string
	vec       []float32
	codes     string
	answer    string
	tenant    string
	version   string
	expiresAt time.Time
}

type answerCache struct {
	mu      sync.Mutex
	entries []cacheEntry
}

func extractCodes(q string) string {
	var codes []string
	for _, tok := range tokens(q) {
		for _, r := range tok {
			if unicode.IsDigit(r) {
				codes = append(codes, tok)
				break
			}
		}
	}
	sort.Strings(codes)
	return strings.Join(codes, ",")
}

func (c *answerCache) get(ctx context.Context, emb embedder, query, tenant, version string) (string, bool) {
	vecs, err := emb.Embed(ctx, []string{query})
	if err != nil || len(vecs) == 0 {
		return "", false
	}
	qVec := vecs[0]
	qCodes := extractCodes(query)
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	bestScore := 0.0
	bestAns := ""
	for _, e := range c.entries {
		if now.After(e.expiresAt) || e.tenant != tenant || e.version != version {
			continue
		}
		if e.codes != qCodes {
			continue
		}
		if sim := embed.Cosine(qVec, e.vec); sim >= cacheSimThreshold && sim > bestScore {
			bestScore = sim
			bestAns = e.answer
		}
	}
	return bestAns, bestAns != ""
}

func (c *answerCache) put(ctx context.Context, emb embedder, query, answer, tenant, version string) {
	vecs, err := emb.Embed(ctx, []string{query})
	if err != nil || len(vecs) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, cacheEntry{
		query:     query,
		vec:       vecs[0],
		codes:     extractCodes(query),
		answer:    answer,
		tenant:    tenant,
		version:   version,
		expiresAt: time.Now().Add(cacheTTL),
	})
}
func normalize(q string) string {
	return strings.Join(tokens(q), " ")
}

// defaultChunks — корпус стартера: ті самі LEDGERWORKS-документи, що в ДЗ 5,
// уже нарізані у форматі labs5.Chunk. Шлях відносний — запускайте з теки labs6.
const defaultChunks = "testdata/chunks.json"

// chunksPath обирає корпус: змінна CHUNKS (ваш індекс із ДЗ 5) або стартовий.
func chunksPath(env string) string {
	if env != "" {
		return env
	}
	return defaultChunks
}

// embedder перетворює тексти на вектори — по одному на текст, у тому ж порядку.
//
// Реалізація — internal/embed: за замовчуванням чистий Go (детермінований
// стаб, без мережі), Ollama — якщо задано OLLAMA_EMBED_MODEL
// (https://docs.ollama.com/capabilities/embeddings). Там же embed.Cosine і
// embed.Rerank. Стаб — лише для wiring і тестів: пороги калібруйте на Ollama.
type embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// retriever тримає корпус, кеш і embedder; його методи — вузли графа.
type retriever struct {
	chunks []corpus.Chunk
	cache  answerCache
	emb    embedder
}

func newRetriever(chunks []corpus.Chunk, emb embedder) *retriever {
	return &retriever{chunks: chunks, emb: emb}
}

// search шукає топ-K кандидатів по чанках із ДЗ 5.
//
// Зараз це BASELINE: частка слів запиту, які є в чанку (keyword overlap).
// Він навмисно наївний — на «яка ставка на тарифі T-2?» чанк про T-1 набирає
// майже стільки ж, скільки чанк про T-2.
//
// TODO(студент): векторний пошук (ембединги) або BM25; для ++ Advanced —
// обидва через fan-out + RRF.
func (r *retriever) search(ctx agent.Context, query string) (SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return SearchResult{}, fmt.Errorf("порожній запит")
	}
	if answer, ok := r.cache.get(ctx, r.emb, query, "default", "v1"); ok {
		return SearchResult{Query: query, CacheHit: true, Candidates: []Candidate{{ChunkID: "cache", Text: answer, Score: 1}}}, nil
	}
	var valid []corpus.Chunk
	texts := []string{query}
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
	qTokens := tokens(query)
	var out []Candidate
	for i, c := range valid {
		cos := embed.Cosine(qVec, vecs[i+1])
		ov := overlap(qTokens, tokens(c.Text))
		if ov == 0 && cos < 0.25 {
			continue
		}
		score := 0.6*cos + 0.4*ov
		out = append(out, Candidate{ChunkID: c.ID, DocumentID: c.DocumentID, SectionPath: c.SectionPath, Text: c.Text, Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > topK {
		out = out[:topK]
	}
	return SearchResult{Query: query, Candidates: out}, nil
}

// rerank переранжовує кандидатів до топ-N.
// TODO(студент): перший крок — embed.Rerank(ctx, r.emb, in.Query, тексти)
// (bi-encoder: cosine між векторами запиту й чанка). Далі — cross-encoder:
// systemone.New().Rerank(ctx, in.Query, тексти) — decision-модель tev1:4b в Ollama
// (P(yes) на «чи відповідає уривок на запит?»; вмикається SYSTEMONE_URL),
// BAAI/bge-reranker-v2-m3 або LLM-судія. Збережіть порядок «до/після» для README.
func (r *retriever) rerank(ctx agent.Context, in SearchResult) (SearchResult, error) {
	if in.CacheHit || len(in.Candidates) <= 1 {
		return in, nil
	}
	texts := make([]string, len(in.Candidates))
	for i, c := range in.Candidates {
		texts[i] = c.Text
	}
	scored, err := embed.Rerank(ctx, r.emb, in.Query, texts)
	if err != nil {
		return SearchResult{}, err
	}
	qTokens := tokens(in.Query)
	reordered := make([]Candidate, len(scored))
	for i, s := range scored {
		c := in.Candidates[s.Index]
		c.Score = s.Score + 0.5*overlap(qTokens, tokens(c.Text))
		reordered[i] = c
	}
	sort.SliceStable(reordered, func(i, j int) bool { return reordered[i].Score > reordered[j].Score })
	return SearchResult{Query: in.Query, Candidates: reordered, CacheHit: in.CacheHit}, nil
}

// answer формує фінальну відповідь із топ-кандидата і пише в кеш.
// TODO(студент): складіть відповідь із кількох кандидатів із цитатами
// chunk_id / document_id; для multi-hop — з обох гілок.
func (r *retriever) answer(ctx agent.Context, in SearchResult) (string, error) {
	if len(in.Candidates) == 0 {
		return "Нічого не знайдено: у корпусі немає доказів для цього запиту.", nil
	}
	if in.CacheHit {
		return in.Candidates[0].Text, nil
	}
	limit := min(3, len(in.Candidates))
	var b strings.Builder
	top := in.Candidates[0]
	fmt.Fprintf(&b, "[cache_hit=false] Топ-результат (%s, %s): %s", top.ChunkID, top.DocumentID, top.Text)
	if limit > 1 {
		b.WriteString("\nДодаткові джерела:")
		for _, c := range in.Candidates[1:limit] {
			fmt.Fprintf(&b, "\n- (%s, %s): %s", c.ChunkID, c.DocumentID, c.Text)
		}
	}
	result := b.String()
	r.cache.put(ctx, r.emb, in.Query, strings.Replace(result, "[cache_hit=false]", "[cache_hit=true]", 1), "default", "v1")
	return result, nil
}

// tokens — нижній регістр, слова з літер/цифр; дефіс лишається всередині
// слова, щоб "T-2" не злився з "T-1".
func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	})
}

// overlap — частка слів запиту, що зустрічаються в тексті чанка.
func overlap(query, text []string) float64 {
	if len(query) == 0 {
		return 0
	}
	in := make(map[string]bool, len(text))
	for _, t := range text {
		in[t] = true
	}
	hits := 0
	for _, q := range query {
		if in[q] {
			hits++
		}
	}
	return float64(hits) / float64(len(query))
}
