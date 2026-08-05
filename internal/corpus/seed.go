package corpus

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/meirongdev/readlist/internal/store"
)

// bookSeed 是演示语料的单本定义(seed 命令的输入,MVP 阶段代替 calibre 快照)。
type bookSeed struct {
	title       string
	author      string
	isbn13      string
	googleID    string
	publisher   string
	format      string
	lang        string
	hasComments bool
	hasCover    bool
	pubdate     string // YYYY-MM-DD
	pubdateSrc  string // google|openlibrary|file-meta|mtime-fallback|unknown
	personal    float64

	gbRating float64 // 0 = 无 Google Books 评分
	gbCount  int
	olRating float64
	olCount  int
	mentions []int // HN 提及距今年数(created_at = now - years)

	topicClass string
	topics     []string
	level      string
	depth      float64
	practical  float64
	labelConf  float64 // 0 = 无标注
	readStatus string  // "" | read | reading
	shelves    []string
}

var seedBooks = []bookSeed{
	// ── 语言核心 ─────────────────────────────────────────────────────
	{title: "The Go Programming Language", author: "Alan A. A. Donovan & Brian W. Kernighan", isbn13: "9780134190440", publisher: "Addison-Wesley Professional", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2015-10-26", pubdateSrc: "google", gbRating: 4.6, gbCount: 480, olRating: 4.5, olCount: 150, mentions: []int{1, 2, 3, 4, 5, 7, 9}, topicClass: "语言核心", topics: []string{"go", "programming-language"}, level: "intermediate", depth: 68, practical: 80, labelConf: 0.92, readStatus: "read", shelves: []string{"精读"}, personal: 5},
	{title: "Learning Go", author: "Jon Bodner", isbn13: "9781492077213", googleID: "b9U8EAAAQBAJ", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2021-03-16", pubdateSrc: "google", gbRating: 4.5, gbCount: 95, olRating: 4.3, olCount: 22, mentions: []int{1, 3}, topicClass: "语言核心", topics: []string{"go"}, level: "beginner", depth: 55, practical: 88, labelConf: 0.9},
	{title: "Learning Go, Second Edition", author: "Jon Bodner", isbn13: "9781098139292", publisher: "O'Reilly Media, Inc.", format: "PDF", lang: "eng", hasComments: true, hasCover: true, pubdate: "2024-03-19", pubdateSrc: "google", gbRating: 4.4, gbCount: 60, olRating: 0, olCount: 0, mentions: []int{1, 2}, topicClass: "语言核心", topics: []string{"go"}, level: "beginner", depth: 56, practical: 86, labelConf: 0.9},
	{title: "Concurrency in Go", author: "Katherine Cox-Buday", isbn13: "9781491941195", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2017-07-25", pubdateSrc: "google", gbRating: 4.3, gbCount: 88, mentions: []int{2, 5, 8}, topicClass: "语言核心", topics: []string{"go", "concurrency"}, level: "intermediate", depth: 72, practical: 78, labelConf: 0.85},
	{title: "100 Go Mistakes and How to Avoid Them", author: "Teiva Harsanyi", isbn13: "9781617299599", publisher: "Manning Publications", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2022-08-30", pubdateSrc: "google", gbRating: 4.6, gbCount: 140, mentions: []int{1, 2, 4}, topicClass: "语言核心", topics: []string{"go"}, level: "intermediate", depth: 60, practical: 90, labelConf: 0.88, readStatus: "read", shelves: []string{"精读"}, personal: 4.5},
	{title: "Fluent Python", author: "Luciano Ramalho", isbn13: "9781491946008", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2015-08-20", pubdateSrc: "google", gbRating: 4.7, gbCount: 320, olRating: 4.6, olCount: 90, mentions: []int{1, 2, 3, 6, 8}, topicClass: "语言核心", topics: []string{"python"}, level: "advanced", depth: 74, practical: 82, labelConf: 0.9},
	{title: "Fluent Python, 2nd Edition", author: "Luciano Ramalho", isbn13: "9781492056355", publisher: "O'Reilly", format: "PDF", lang: "eng", hasComments: true, hasCover: true, pubdate: "2022-04-12", pubdateSrc: "openlibrary", gbRating: 4.6, gbCount: 120, mentions: []int{1, 3}, topicClass: "语言核心", topics: []string{"python"}, level: "advanced", depth: 75, practical: 80, labelConf: 0.9},
	{title: "Python Crash Course", author: "Eric Matthes", isbn13: "9781593279288", publisher: "No Starch Press", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2019-05-03", pubdateSrc: "google", gbRating: 4.5, gbCount: 210, mentions: []int{2, 4, 9}, topicClass: "语言核心", topics: []string{"python"}, level: "beginner", depth: 40, practical: 95, labelConf: 0.93},
	{title: "Automate the Boring Stuff with Python", author: "Al Sweigart", isbn13: "9781593279929", publisher: "No Starch Press", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2019-11-12", pubdateSrc: "google", gbRating: 4.6, gbCount: 260, olRating: 4.5, olCount: 110, mentions: []int{2, 5}, topicClass: "语言核心", topics: []string{"python", "automation"}, level: "beginner", depth: 30, practical: 98, labelConf: 0.92, readStatus: "read", shelves: []string{"想读"}, personal: 4.5},
	{title: "The Rust Programming Language", author: "Steve Klabnik & Carol Nichols", isbn13: "9781718503106", publisher: "No Starch Press", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2019-08-13", pubdateSrc: "google", gbRating: 4.6, gbCount: 180, mentions: []int{1, 2, 3, 5}, topicClass: "语言核心", topics: []string{"rust"}, level: "intermediate", depth: 66, practical: 84, labelConf: 0.9},
	{title: "Zero to Production in Rust", author: "Luca Palmieri", isbn13: "9789083381714", publisher: "self-published", format: "PDF", lang: "eng", hasComments: true, hasCover: true, pubdate: "2023-02-01", pubdateSrc: "file-meta", gbRating: 0, gbCount: 0, mentions: []int{1, 2}, topicClass: "语言核心", topics: []string{"rust", "web"}, level: "intermediate", depth: 70, practical: 85, labelConf: 0.8},

	// ── 系统/分布式/算法(常青)───────────────────────────────────────
	{title: "Designing Data-Intensive Applications", author: "Martin Kleppmann", isbn13: "9781449373320", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2017-03-16", pubdateSrc: "google", gbRating: 4.8, gbCount: 620, olRating: 4.7, olCount: 210, mentions: []int{1, 2, 3, 4, 5, 6, 7, 8}, topicClass: "常青/理论", topics: []string{"distributed-systems", "databases"}, level: "advanced", depth: 88, practical: 70, labelConf: 0.94, readStatus: "read", shelves: []string{"精读"}, personal: 5},
	{title: "Introduction to Algorithms", author: "Thomas H. Cormen & Charles E. Leiserson", isbn13: "9780262033848", publisher: "MIT Press", format: "PDF", lang: "eng", hasComments: false, hasCover: true, pubdate: "2009-07-31", pubdateSrc: "google", gbRating: 4.4, gbCount: 150, mentions: []int{3, 6, 10}, topicClass: "常青/理论", topics: []string{"algorithms"}, level: "advanced", depth: 95, practical: 55, labelConf: 0.91},
	{title: "The Algorithm Design Manual", author: "Steven S. Skiena", isbn13: "9783030542559", publisher: "Springer", format: "PDF", lang: "eng", hasComments: false, hasCover: true, pubdate: "2020-10-06", pubdateSrc: "google", gbRating: 4.3, gbCount: 70, mentions: []int{2, 5}, topicClass: "常青/理论", topics: []string{"algorithms"}, level: "intermediate", depth: 78, practical: 65, labelConf: 0.85},
	{title: "Grokking Algorithms", author: "Aditya Bhargava", isbn13: "9781617292231", publisher: "Manning Publications", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2016-05-15", pubdateSrc: "google", gbRating: 4.5, gbCount: 340, mentions: []int{2, 3, 7}, topicClass: "常青/理论", topics: []string{"algorithms"}, level: "beginner", depth: 45, practical: 80, labelConf: 0.9},
	{title: "Structure and Interpretation of Computer Programs", author: "Harold Abelson & Gerald Jay Sussman", isbn13: "9780262510875", publisher: "MIT Press", format: "PDF", lang: "eng", hasComments: false, hasCover: true, pubdate: "1996-07-25", pubdateSrc: "openlibrary", gbRating: 4.4, gbCount: 90, olRating: 4.5, olCount: 60, mentions: []int{5, 8, 12}, topicClass: "常青/理论", topics: []string{"programming-language", "fundamentals"}, level: "advanced", depth: 90, practical: 60, labelConf: 0.88},
	{title: "Compilers: Principles, Techniques, and Tools", author: "Alfred V. Aho & Monica S. Lam", isbn13: "9780321486813", publisher: "Addison-Wesley", format: "PDF", lang: "eng", hasComments: false, hasCover: true, pubdate: "2006-09-01", pubdateSrc: "google", gbRating: 4.1, gbCount: 55, mentions: []int{6, 11}, topicClass: "常青/理论", topics: []string{"compilers"}, level: "advanced", depth: 96, practical: 50, labelConf: 0.87},
	{title: "Crafting Interpreters", author: "Robert Nystrom", isbn13: "9780990582939", publisher: "Genever Benning", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2021-07-27", pubdateSrc: "google", gbRating: 4.8, gbCount: 200, olRating: 4.7, olCount: 80, mentions: []int{1, 2, 3}, topicClass: "常青/理论", topics: []string{"compilers"}, level: "advanced", depth: 92, practical: 72, labelConf: 0.93},
	{title: "Operating Systems: Three Easy Pieces", author: "Remzi H. Arpaci-Dusseau", isbn13: "9781985086593", publisher: "self-published", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2015-08-18", pubdateSrc: "file-meta", gbRating: 4.4, gbCount: 110, mentions: []int{3, 7}, topicClass: "常青/理论", topics: []string{"operating-system"}, level: "intermediate", depth: 82, practical: 58, labelConf: 0.86},
	{title: "The Linux Programming Interface", author: "Michael Kerrisk", isbn13: "9781593272203", publisher: "No Starch Press", format: "PDF", lang: "eng", hasComments: false, hasCover: true, pubdate: "2010-10-28", pubdateSrc: "google", gbRating: 4.7, gbCount: 75, mentions: []int{4, 8}, topicClass: "常青/理论", topics: []string{"linux", "systems"}, level: "advanced", depth: 90, practical: 68, labelConf: 0.84},

	// ── 分布式/云原生(平台,半衰期 5 年)─────────────────────────────
	{title: "Kubernetes in Action", author: "Marko Luksa", isbn13: "9781617293726", publisher: "Manning Publications", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2018-01-12", pubdateSrc: "google", gbRating: 4.6, gbCount: 230, mentions: []int{2, 4, 6}, topicClass: "平台/生态", topics: []string{"kubernetes"}, level: "intermediate", depth: 72, practical: 80, labelConf: 0.9, readStatus: "read", shelves: []string{"想读"}, personal: 4},
	{title: "Kubernetes in Action, 2nd Edition", author: "Marko Luksa & Manuel Pais", isbn13: "9781617297618", publisher: "Manning", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2023-12-05", pubdateSrc: "google", gbRating: 4.5, gbCount: 90, mentions: []int{1}, topicClass: "平台/生态", topics: []string{"kubernetes"}, level: "intermediate", depth: 74, practical: 82, labelConf: 0.88},
	{title: "Cloud Native DevOps with Kubernetes", author: "John Arundel & Justin Domingus", isbn13: "9781492040767", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2019-04-16", pubdateSrc: "google", gbRating: 4.4, gbCount: 85, mentions: []int{2, 5}, topicClass: "平台/生态", topics: []string{"kubernetes", "devops"}, level: "intermediate", depth: 64, practical: 86, labelConf: 0.85},
	{title: "Building Microservices", author: "Sam Newman", isbn13: "9781492034025", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2015-02-20", pubdateSrc: "google", gbRating: 4.3, gbCount: 190, mentions: []int{3, 5, 8}, topicClass: "平台/生态", topics: []string{"microservices", "architecture"}, level: "intermediate", depth: 66, practical: 74, labelConf: 0.87},
	{title: "Kafka: The Definitive Guide", author: "Gwen Shapira & Todd Palino", isbn13: "9781492043089", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2017-10-30", pubdateSrc: "google", gbRating: 4.3, gbCount: 60, mentions: []int{3, 6}, topicClass: "平台/生态", topics: []string{"kafka", "data-engineering"}, level: "intermediate", depth: 68, practical: 72, labelConf: 0.82},
	{title: "Database Internals", author: "Alex Petrov", isbn13: "9781492040347", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2019-10-25", pubdateSrc: "google", gbRating: 4.6, gbCount: 130, mentions: []int{1, 2, 4}, topicClass: "常青/理论", topics: []string{"databases", "storage"}, level: "advanced", depth: 90, practical: 62, labelConf: 0.89},
	{title: "Designing Distributed Systems", author: "Brendan Burns", isbn13: "9781491983645", publisher: "O'Reilly Media", format: "PDF", lang: "eng", hasComments: true, hasCover: true, pubdate: "2018-02-21", pubdateSrc: "google", gbRating: 4.1, gbCount: 65, mentions: []int{3}, topicClass: "平台/生态", topics: []string{"distributed-systems", "kubernetes"}, level: "beginner", depth: 58, practical: 76, labelConf: 0.8},
	{title: "Distributed Systems", author: "Maarten van Steen & Andrew S. Tanenbaum", isbn13: "9781543057386", publisher: "self-published", format: "PDF", lang: "eng", hasComments: false, hasCover: true, pubdate: "2017-02-01", pubdateSrc: "file-meta", gbRating: 4.2, gbCount: 40, mentions: []int{4}, topicClass: "常青/理论", topics: []string{"distributed-systems"}, level: "intermediate", depth: 76, practical: 55, labelConf: 0.78},

	// ── 软件工程/方法(常青~平台)──────────────────────────────────
	{title: "Clean Code", author: "Robert C. Martin", isbn13: "9780132350884", publisher: "Prentice Hall", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2008-08-01", pubdateSrc: "google", gbRating: 4.4, gbCount: 400, olRating: 4.3, olCount: 160, mentions: []int{2, 3, 5, 7}, topicClass: "常青/理论", topics: []string{"software-craft"}, level: "intermediate", depth: 58, practical: 84, labelConf: 0.88, readStatus: "read", personal: 4},
	{title: "Refactoring, 2nd Edition", author: "Martin Fowler", isbn13: "9780134757599", publisher: "Addison-Wesley", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2018-11-19", pubdateSrc: "google", gbRating: 4.5, gbCount: 170, mentions: []int{2, 4}, topicClass: "常青/理论", topics: []string{"refactoring", "software-craft"}, level: "intermediate", depth: 62, practical: 86, labelConf: 0.87},
	{title: "The Pragmatic Programmer, 2nd Edition", author: "David Thomas & Andrew Hunt", isbn13: "9780135957059", publisher: "Addison-Wesley", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2019-09-13", pubdateSrc: "google", gbRating: 4.5, gbCount: 260, mentions: []int{2, 5}, topicClass: "常青/理论", topics: []string{"software-craft"}, level: "intermediate", depth: 60, practical: 82, labelConf: 0.9},
	{title: "Effective Java, 3rd Edition", author: "Joshua Bloch", isbn13: "9780134685991", publisher: "Addison-Wesley", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2018-01-06", pubdateSrc: "google", gbRating: 4.6, gbCount: 220, mentions: []int{2, 6}, topicClass: "语言核心", topics: []string{"java"}, level: "advanced", depth: 76, practical: 80, labelConf: 0.89},

	// ── 框架/版本(半衰期 2.5 年)──────────────────────────────────
	{title: "Spring in Action, 6th Edition", author: "Craig Walls", isbn13: "9781617297571", publisher: "Manning Publications", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2022-05-17", pubdateSrc: "google", gbRating: 4.4, gbCount: 95, mentions: []int{2}, topicClass: "框架/版本", topics: []string{"spring", "java"}, level: "intermediate", depth: 62, practical: 84, labelConf: 0.85},
	{title: "Learning Spring Boot 3.0", author: "Greg L. Turnquist", isbn13: "9781803233307", publisher: "Packt Publishing", format: "PDF", lang: "eng", hasComments: true, hasCover: true, pubdate: "2022-12-30", pubdateSrc: "google", gbRating: 4.2, gbCount: 30, topicClass: "框架/版本", topics: []string{"spring", "java"}, level: "beginner", depth: 52, practical: 86, labelConf: 0.75},
	{title: "Go Web Programming", author: "Sau Sheong Chang", isbn13: "9781617292569", publisher: "Manning Publications", format: "PDF", lang: "eng", hasComments: false, hasCover: true, pubdate: "2016-07-30", pubdateSrc: "google", gbRating: 4.0, gbCount: 45, topicClass: "框架/版本", topics: []string{"go", "web"}, level: "intermediate", depth: 60, practical: 82, labelConf: 0.78},
	{title: "RESTful Web APIs", author: "Leonard Richardson", isbn13: "9781449358068", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2013-09-30", pubdateSrc: "mtime-fallback", gbRating: 4.3, gbCount: 80, mentions: []int{3}, topicClass: "框架/版本", topics: []string{"rest", "web"}, level: "beginner", depth: 50, practical: 80, labelConf: 0.8}, // F 未知 → D

	// ── AI / LLM(时事,半衰期 1.5 年)───────────────────────────────
	{title: "Designing Machine Learning Systems", author: "Chip Huyen", isbn13: "9781098107963", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2022-06-21", pubdateSrc: "google", gbRating: 4.7, gbCount: 180, olRating: 4.6, olCount: 60, mentions: []int{1, 2, 3}, topicClass: "时事/趋势", topics: []string{"ml", "mlops"}, level: "intermediate", depth: 70, practical: 82, labelConf: 0.88},
	{title: "Deep Learning", author: "Ian Goodfellow & Yoshua Bengio", isbn13: "9780262035613", publisher: "MIT Press", format: "PDF", lang: "eng", hasComments: false, hasCover: true, pubdate: "2016-11-18", pubdateSrc: "google", gbRating: 4.4, gbCount: 140, mentions: []int{2, 5}, topicClass: "常青/理论", topics: []string{"deep-learning", "ai"}, level: "advanced", depth: 95, practical: 55, labelConf: 0.86},
	{title: "Hands-On Machine Learning with Scikit-Learn, Keras & TensorFlow", author: "Aurélien Géron", isbn13: "9781492032649", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2019-10-15", pubdateSrc: "google", gbRating: 4.7, gbCount: 300, mentions: []int{2, 4}, topicClass: "时事/趋势", topics: []string{"ml", "python"}, level: "intermediate", depth: 68, practical: 88, labelConf: 0.9},
	{title: "Build a Large Language Model (From Scratch)", author: "Sebastian Raschka", isbn13: "9781633437166", publisher: "Manning Publications", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2024-10-29", pubdateSrc: "google", gbRating: 4.8, gbCount: 260, olRating: 4.7, olCount: 90, mentions: []int{1, 1, 2}, topicClass: "时事/趋势", topics: []string{"llm", "ai", "deep-learning"}, level: "intermediate", depth: 80, practical: 88, labelConf: 0.92, readStatus: "reading"},
	{title: "AI Engineering", author: "Chip Huyen", isbn13: "9781098166304", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2026-01-07", pubdateSrc: "google", gbRating: 4.6, gbCount: 40, mentions: []int{1}, topicClass: "时事/趋势", topics: []string{"llm", "ai", "mlops"}, level: "intermediate", depth: 74, practical: 84, labelConf: 0.83}, // 2026 新书
	{title: "Generative AI with LangChain", author: "Ben Auffarth", isbn13: "9781835083468", publisher: "Packt", format: "PDF", lang: "eng", hasComments: false, hasCover: true, pubdate: "2023-05-31", pubdateSrc: "google", gbRating: 3.9, gbCount: 25, topicClass: "时事/趋势", topics: []string{"llm", "ai", "langchain"}, level: "beginner", depth: 48, practical: 80, labelConf: 0.7},
	{title: "Prompt Engineering for LLMs", author: "John Berryman & Albert Ziegler", isbn13: "9781835082232", publisher: "Packt Publishing Ltd", format: "PDF", lang: "eng", hasComments: false, hasCover: false, pubdate: "2024-05-17", pubdateSrc: "google", gbRating: 4.0, gbCount: 18, topicClass: "时事/趋势", topics: []string{"llm", "prompt"}, level: "beginner", depth: 42, practical: 85, labelConf: 0.72},
	{title: "The Little Book of Deep Learning", author: "François Fleuret", isbn13: "9798886801344", publisher: "self-published", format: "PDF", lang: "eng", hasComments: false, hasCover: false, pubdate: "2024-03-01", pubdateSrc: "file-meta", gbRating: 0, gbCount: 0, mentions: []int{1}, topicClass: "时事/趋势", topics: []string{"deep-learning", "ai"}, level: "intermediate", depth: 72, practical: 40, labelConf: 0.68},

	// ── 数据/数据库 ──────────────────────────────────────────────
	{title: "SQL Antipatterns", author: "Bill Karwin", isbn13: "9781934356555", publisher: "Pragmatic Bookshelf", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2010-05-18", pubdateSrc: "google", gbRating: 4.2, gbCount: 60, mentions: []int{4, 7}, topicClass: "常青/理论", topics: []string{"sql", "databases"}, level: "intermediate", depth: 58, practical: 78, labelConf: 0.83},
	{title: "High Performance MySQL", author: "Baron Schwartz & Peter Zaitsev", isbn13: "9781449332471", publisher: "O'Reilly Media", format: "EPUB", lang: "eng", hasComments: true, hasCover: true, pubdate: "2012-04-13", pubdateSrc: "google", gbRating: 4.5, gbCount: 55, mentions: []int{4}, topicClass: "常青/理论", topics: []string{"mysql", "databases"}, level: "advanced", depth: 82, practical: 70, labelConf: 0.8},
	{title: "Database System Concepts", author: "Abraham Silberschatz & Henry F. Korth", isbn13: "9780078022159", publisher: "McGraw-Hill Education", format: "PDF", lang: "eng", hasComments: false, hasCover: true, pubdate: "2019-02-19", pubdateSrc: "openlibrary", gbRating: 4.0, gbCount: 35, topicClass: "常青/理论", topics: []string{"databases"}, level: "advanced", depth: 88, practical: 48, labelConf: 0.8},

	// ── 边缘案例:未知作者 / 中文书 / 未来日期 / 低置信标注 ────────
	{title: "The Mystery Systems Book", author: "Unknown", isbn13: "9780000000001", publisher: "self-published", format: "PDF", lang: "eng", hasComments: false, hasCover: false, pubdate: "2015-06-01", pubdateSrc: "mtime-fallback", gbRating: 0, gbCount: 0, topicClass: "常青/理论", topics: []string{"systems"}, level: "beginner", depth: 0, practical: 0, labelConf: 0.0}, // 作者 Unknown + F 未知 → D
	{title: "Go 语言高并发与微服务实战", author: "朱洪波", isbn13: "9787121418038", publisher: "电子工业出版社", format: "EPUB", lang: "zho", hasComments: true, hasCover: true, pubdate: "2021-08-01", pubdateSrc: "google", gbRating: 4.1, gbCount: 12, topicClass: "平台/生态", topics: []string{"go", "microservices"}, level: "intermediate", depth: 66, practical: 78, labelConf: 0.75},
	{title: "深入理解计算机系统(第3版)", author: "Randal E. Bryant", isbn13: "9787111544937", publisher: "机械工业出版社", format: "EPUB", lang: "zho", hasComments: true, hasCover: true, pubdate: "2016-11-01", pubdateSrc: "google", gbRating: 4.8, gbCount: 85, topicClass: "常青/理论", topics: []string{"systems", "csapp"}, level: "advanced", depth: 90, practical: 62, labelConf: 0.87, readStatus: "read", shelves: []string{"想读"}, personal: 5},
	{title: "Kubernetes in 2027", author: "Futurist Author", isbn13: "9789999999999", publisher: "Packt", format: "PDF", lang: "eng", hasComments: false, hasCover: false, pubdate: "2027-01-01", pubdateSrc: "file-meta", gbRating: 0, gbCount: 0, topicClass: "平台/生态", topics: []string{"kubernetes"}, level: "beginner", depth: 0, practical: 0, labelConf: 0.0},                         // 未来日期 → F unknown → D
	{title: "Rust for the Impatient (Draft Notes)", author: "Ada Lovelace", isbn13: "9780000000002", publisher: "self-published", format: "PDF", lang: "eng", hasComments: false, hasCover: false, pubdate: "2022-05-01", pubdateSrc: "file-meta", gbRating: 0, gbCount: 0, topicClass: "语言核心", topics: []string{"rust"}, level: "intermediate", depth: 55, practical: 70, labelConf: 0.42}, // 低置信 → D/P 未知 → 只进目录
	{title: "BPB Rust Cookbook", author: "R. Sharma", isbn13: "9788183333333", publisher: "BPB Publications", format: "PDF", lang: "eng", hasComments: false, hasCover: false, pubdate: "2020-02-01", pubdateSrc: "file-meta", gbRating: 0, gbCount: 0, mentions: []int{3}, topicClass: "语言核心", topics: []string{"rust"}, level: "beginner", depth: 40, practical: 85, labelConf: 0.6},
}

// Seed 向空库写入演示语料(幂等:已有书则跳过)。返回写入的 edition 数。
func Seed(d *store.DB) (int, error) {
	db := d.SQL()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM editions`).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}

	now := time.Now().UTC()
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	seenWorks := map[string]bool{}
	written := 0
	for _, b := range seedBooks {
		workID := WorkKey(b.title, b.author)
		if !seenWorks[workID] {
			seenWorks[workID] = true
			hl := HalfLifeFor(b.title, b.topicClass, nil)
			if _, err := tx.Exec(`INSERT OR IGNORE INTO works
				(work_id, canonical_title, first_author, primary_topic, level, half_life_years, half_life_source)
				VALUES (?,?,?,?,?,?,?)`,
				workID, b.title, b.author, b.topicClass, b.level, hl.Years, hl.Source); err != nil {
				return 0, err
			}
		}

		pi := Publisher(b.publisher)
		// 出版社归一表(人工投入,不可再生 → 必须夜备)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO publisher_map (raw, norm, tier) VALUES (?,?,?)`,
			b.publisher, pi.Norm, pi.Tier); err != nil {
			return 0, err
		}

		if _, err := tx.Exec(`INSERT INTO editions
			(book_id, work_id, title, isbn13, google_volume_id, publisher_raw, publisher_norm, format,
			 language, has_comments, has_cover, pubdate, pubdate_source, personal_rating_stars)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			written+1, workID, b.title, nullable(b.isbn13), nullable(b.googleID), b.publisher, pi.Norm, b.format,
			b.lang, boolInt(b.hasComments), boolInt(b.hasCover), nullable(b.pubdate), b.pubdateSrc,
			nullableFloat(b.personal)); err != nil {
			return 0, err
		}

		// 外部证据:Google Books / OpenLibrary,原样存 JSON + fetched_at(TTL 180 天)。
		if b.gbCount > 0 {
			payload, _ := json.Marshal(map[string]any{"rating": b.gbRating, "count": b.gbCount})
			srcID := b.googleID
			if srcID == "" {
				srcID = "isbn:" + b.isbn13
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO evidence (source, source_id, work_id, payload, fetched_at, ttl_days)
				VALUES ('google_books', ?, ?, ?, ?, 180)`, srcID, workID, string(payload), now.Format(time.RFC3339)); err != nil {
				return 0, err
			}
		}
		if b.olCount > 0 {
			payload, _ := json.Marshal(map[string]any{"rating": b.olRating, "count": b.olCount})
			if _, err := tx.Exec(`INSERT OR IGNORE INTO evidence (source, source_id, work_id, payload, fetched_at, ttl_days)
				VALUES ('openlibrary', ?, ?, ?, ?, 180)`, "isbn:"+b.isbn13, workID, string(payload), now.Format(time.RFC3339)); err != nil {
				return 0, err
			}
		}

		// HN 提及(保留 objectID,可人工否决)。
		for i, y := range b.mentions {
			objID := fmt.Sprintf("hn-%s-%d", workID, i)
			created := now.AddDate(-y, 0, 0).Format(time.RFC3339)
			if _, err := tx.Exec(`INSERT OR IGNORE INTO mentions (work_id, object_id, created_at, matched_by)
				VALUES (?,?,?, 'exact-title+author')`, workID, objID, created); err != nil {
				return 0, err
			}
		}

		// LLM/人工标注(带输入指纹)。
		if b.labelConf > 0 {
			topics, _ := json.Marshal(b.topics)
			fp := fmt.Sprintf("%x", len(b.title)+len(b.author)+int(b.labelConf*100))
			if _, err := tx.Exec(`INSERT OR IGNORE INTO labels
				(work_id, topic_class, topics, level, depth, practicality, confidence, input_fingerprint, labeled_by, labeled_at)
				VALUES (?,?,?,?,?,?,?,?, 'demo-seed', ?)`,
				workID, b.topicClass, string(topics), b.level, b.depth, b.practical, b.labelConf, fp, now.Format(time.RFC3339)); err != nil {
				return 0, err
			}
		}

		// 阅读状态(只读镜像:status/shelves;MVP 的 demo 数据直接来自这里)。
		if b.readStatus != "" || len(b.shelves) > 0 {
			shelves, _ := json.Marshal(b.shelves)
			status := b.readStatus
			if status == "" {
				status = "read" // 有书架无显式状态按已读处理(简化 demo)
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO reading (book_id, status, shelves, downloads, last_modified)
				VALUES (?,?,?,?,?)`, written+1, status, string(shelves), 0, now.Format(time.RFC3339)); err != nil {
				return 0, err
			}
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return written, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}
