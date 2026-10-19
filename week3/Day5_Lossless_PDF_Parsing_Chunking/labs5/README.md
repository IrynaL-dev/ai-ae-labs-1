# Лабораторна 5 — ingestion-конвеєр: PDF → Markdown → ієрархічні чанки

**Станом на 09/2026:** Go 1.27.1, ADK Go v2.5.0, docling-mcp 3.2.1. Жодного LLM, ключа або `.env`.
Завдання — у [Homework.md](Homework.md).

## Запуск

```bash
cd week3/Day5_Lossless_PDF_Parsing_Chunking/labs5
go run . console          # введіть: testdata/ledgerworks_soc2.md
go test -v .              # 9 passed, 1 skipped — контракт TestChunk_TableStaysWhole
```

Граф (`graph.go`):

```text
Start → load → parse → chunk → extract_entities → index_vector → index_graph → report
```

| Файл | Що в ньому | Ваша робота |
|---|---|---|
| `pipeline.go` | вузли й типи `Source`, `Document`, `Chunk`, `Entity`, `Batch`, `Stores` | `TODO(студент)`: `chunk`, `extract_entities`, `index_*`, `report` |
| `graph.go` | топологія | ++ A3: fan-out на кілька документів |
| `main.go` | лише wiring: docling-клієнт + launcher | — |
| `main_test.go` | табличні тести на `StrictContextMock` | зняти `t.Skip`, переписати тести-заглушки |
| `internal/docling` | MCP-клієнт до docling-mcp | читати, не змінювати |
| `internal/embed` | ембединги (чистий Go за замовчуванням, Ollama — через `OLLAMA_EMBED_MODEL`), `Cosine`, `Rerank` | у `index_vector`, A1 |
| `internal/mdblocks` | Markdown → блоки (`Document.Blocks`); таблиця — один блок; глибина заголовка з нумерації, а не з `#` | у `chunk`, `extract_entities` |
| `internal/chunkeval` | токени, min/avg/max, скільки таблиць цілі, naive baseline | у `report`, порівняння для README |
| `internal/vecindex` | in-memory індекс зі стаб-ембедингом | у `index_vector`, A1 |
| `internal/kgraph` | in-memory граф із provenance | у `index_graph` |
| `testdata/` | тестовий документ: `.md`, `.pdf` і реальний вихід docling `.docling.md` | додати свій PDF |

## PDF через docling-mcp

`parse` передає PDF у [docling-mcp](https://github.com/docling-project/docling-mcp) — MCP-сервер над
[docling](https://github.com/docling-project/docling), який розпізнає розмітку сторінки й таблиці. Клієнт —
офіційний Go SDK `github.com/modelcontextprotocol/go-sdk` через stdio; два виклики на документ:

```text
convert_document_into_docling_document(source)  → document_key
export_docling_document_to_markdown(document_key) → markdown
```

**Що потрібно:** [`uv`](https://docs.astral.sh/uv/getting-started/installation/) (у dev-контейнері вже є). Сервер
стартує сам, лише коли приходить перший PDF:

```bash
uvx --from='docling-mcp[local]' docling-mcp-server --transport stdio
```

Перший запуск завантажує docling і моделі розмітки (сотні MB, кілька хвилин; прогрес — у stderr).

| Змінна | За замовчуванням | Навіщо |
|---|---|---|
| `DOCLING_MCP_CMD` | команда вище | свій сервер або пін версії: `uvx --from=docling-mcp[local]==3.2.1 docling-mcp-server --transport stdio` |
| `DOCLING_MCP_CONVERSION_MODE` | `local` | `remote` + `DOCLING_MCP_SERVICE_URL` — конвертація через Docling Serve |
| `DOCLING_MCP_DO_OCR` | `false` | `true` для сканованого PDF без текстового шару (завантажить OCR-моделі) |

Перевірити справжній сервер на тестовому PDF (з кореня репозиторію):

```bash
DOCLING_E2E=1 go test -run TestRealServer -v ./week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/docling
```

Звичайний `go test ./...` docling не запускає: PDF-шлях у тестах лаби йде через фейковий конвертер.

## Що видно на тестовому PDF

`testdata/ledgerworks_soc2.docling.md` — справжній вихід docling для `ledgerworks_soc2.pdf`:

- **3/3 таблиці цілі**, із заголовками стовпчиків — lossless-частина працює.
- **Рівні заголовків сплющено**: і `1 Scope`, і `1.1 Vendors in scope` стають `##`. Parent-Child
  ієрархію доведеться відновлювати з нумерації розділів, а не з кількості `#`.

## Експорт для ДЗ 6

ДЗ 6 читає масив `Chunk` у JSON (поля фіксує `TestChunkJSONContract`). Збережіть свої чанки й
запустіть ДЗ 6 так: `CHUNKS=…/chunks.json go run . console`.

=== Ingestion Pipeline Report ===
Document: ledgerworks_soc2
Chunks:   33 total (12 parents, 21 children)
Tokens:   min=1, avg=12.5, max=78
Tables:   3/3 preserved (Lossless)
Entities: 16 extracted, Vectors added: 21, Graph nodes added: 9

### Критерій,(А) Naive Chunking (Фіксоване вікно 800 токенів),(Б) Структурний Parser + Parent-Child (Наш пайплайн)

Цілісність таблиць,"Розрізано. Символьний ліміт розриває Markdown-таблицю навпіл (наприклад, у першому чанку залишається шапка Vendor | Risk, а в наступному — рядки даних без заголовків).",Ціла (100% збережено). Кожна таблиця визначена як неподільний блок (BlockTable) і загортається в окремий child-чанк з успадкованим контекстом розділу.
Зв'язок із контекстом (Provenance),"Втрачено. Чанки з даними не знають, до якого розділу чи підрозділу документа вони належали.","Збережено. Завдяки ієрархії кожен child-чанк має ParentID та SectionPath (наприклад, 1. Access Control / 1.1 Identity)."
Якість пошуку (Retrieval),"Модель галюцинує або видає відповідь «не знайдено», оскільки ключові стовпчики відірвані від значень.","Векторний та графовий пошук знаходять релевантні сутності безпосередньо зі збереженими атрибутами (Vendor, Finding ID, Severity)."

### Приклад розрізненої таблиці при Naive Approach (Чому це важливо)
Що бачить RAG при Naive Chunking (Розрив чанку):
Чанк #1: \| Vendor \| Compliance Status \| (Заголовок є, дані відсутні)

Чанк #2: \| Active Directory \| Compliant \| (Дані є, контекст і заголовок втрачені)

### Що бачить RAG у нашому пайплайні:
Child Chunk (Kind == table):
| Vendor / Service | MFA Enforced? | Compliance Status |
| --- | --- | --- |
| Active Directory | Yes (FIDO2 / TOTP) | Compliant |

Прив'язка: ParentID: ledgerworks_soc2-parent-2, SectionPath: 1. Access Control / 1.1 Identity.