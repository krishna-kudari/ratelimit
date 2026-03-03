package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"

	goratelimit "github.com/krishna-kudari/ratelimit"
)

//go:embed static
var staticFS embed.FS

//go:embed templates
var templateFS embed.FS

type configField struct {
	Name    string         `json:"name"`
	Label   string         `json:"label"`
	Default interface{}    `json:"default"`
	Min     float64        `json:"min,omitempty"`
	Max     float64        `json:"max,omitempty"`
	Step    float64        `json:"step,omitempty"`
	Options []selectOption `json:"options,omitempty"`
}

type selectOption struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Selected bool   `json:"-"`
}

type algorithmMeta struct {
	Name         string        `json:"name"`
	Slug         string        `json:"slug"`
	Description  string        `json:"description"`
	RedisType    string        `json:"redisType"`
	Commands     string        `json:"commands"`
	ShortDesc    string        `json:"shortDesc"`
	ConfigFields []configField `json:"configFields"`
}

var algorithms = []algorithmMeta{
	{
		Name:        "Fixed Window Counter",
		Slug:        "fixed-window",
		Description: "Counts requests in fixed time windows using INCR + EXPIRE. Simple but susceptible to boundary bursts.",
		RedisType:   "STRING",
		Commands:    "INCR, EXPIRE, TTL",
		ShortDesc:   "Fixed time intervals",
		ConfigFields: []configField{
			{Name: "maxRequests", Label: "Max Requests", Default: 10, Min: 1, Max: 50, Step: 1},
			{Name: "windowSeconds", Label: "Window (seconds)", Default: 10, Min: 1, Max: 60, Step: 1},
		},
	},
	{
		Name:        "Sliding Window Log",
		Slug:        "sliding-window-log",
		Description: "Logs each request timestamp in a sorted set. Precise sliding window, but stores every request.",
		RedisType:   "SORTED SET",
		Commands:    "ZADD, ZREMRANGEBYSCORE, ZCARD",
		ShortDesc:   "Exact timestamp tracking",
		ConfigFields: []configField{
			{Name: "maxRequests", Label: "Max Requests", Default: 10, Min: 1, Max: 50, Step: 1},
			{Name: "windowSeconds", Label: "Window (seconds)", Default: 10, Min: 1, Max: 60, Step: 1},
		},
	},
	{
		Name:        "Sliding Window Counter",
		Slug:        "sliding-window-counter",
		Description: "Weighted average of current and previous window counts. Smooths the fixed-window boundary problem with minimal memory.",
		RedisType:   "STRING x2",
		Commands:    "INCR, EXPIRE, GET",
		ShortDesc:   "Weighted window blending",
		ConfigFields: []configField{
			{Name: "maxRequests", Label: "Max Requests", Default: 10, Min: 1, Max: 50, Step: 1},
			{Name: "windowSeconds", Label: "Window (seconds)", Default: 10, Min: 1, Max: 60, Step: 1},
		},
	},
	{
		Name:        "Token Bucket",
		Slug:        "token-bucket",
		Description: "Tokens refill at a steady rate; each request consumes one. Allows short bursts up to bucket capacity.",
		RedisType:   "HASH + Lua",
		Commands:    "EVAL, HSET, HGETALL",
		ShortDesc:   "Steady refill, burst-friendly",
		ConfigFields: []configField{
			{Name: "maxTokens", Label: "Max Tokens", Default: 10, Min: 1, Max: 50, Step: 1},
			{Name: "refillRate", Label: "Refill Rate (tok/s)", Default: 1, Min: 1, Max: 10, Step: 1},
		},
	},
	{
		Name:        "Leaky Bucket",
		Slug:        "leaky-bucket",
		Description: "Requests fill a bucket that leaks at a constant rate. Policing drops excess requests; shaping queues them with a delay.",
		RedisType:   "HASH + Lua",
		Commands:    "EVAL, HSET, HGETALL",
		ShortDesc:   "Constant drain rate",
		ConfigFields: []configField{
			{
				Name: "mode", Label: "Mode", Default: "policing",
				Options: []selectOption{
					{Value: "policing", Label: "Policing (drop)", Selected: true},
					{Value: "shaping", Label: "Shaping (queue)"},
				},
			},
			{Name: "capacity", Label: "Capacity", Default: 10, Min: 1, Max: 50, Step: 1},
			{Name: "leakRate", Label: "Leak Rate (req/s)", Default: 1, Min: 1, Max: 10, Step: 1},
		},
	},
	{
		Name:        "GCRA",
		Slug:        "gcra",
		Description: "Generic Cell Rate Algorithm. Enforces a sustained rate with burst allowance via virtual scheduling (TAT).",
		RedisType:   "HASH + Lua",
		Commands:    "EVAL, HSET, HGETALL",
		ShortDesc:   "Virtual scheduling",
		ConfigFields: []configField{
			{Name: "rate", Label: "Rate (req/s)", Default: 5, Min: 1, Max: 50, Step: 1},
			{Name: "burst", Label: "Burst", Default: 10, Min: 1, Max: 50, Step: 1},
		},
	},
}

var algoBySlug map[string]algorithmMeta

func init() {
	algoBySlug = make(map[string]algorithmMeta, len(algorithms))
	for _, a := range algorithms {
		algoBySlug[a.Slug] = a
	}
}

type limiterEntry struct {
	limiter    goratelimit.Limiter
	configHash string
}

var (
	sessions   sync.Map // sessionID -> map[algo]*limiterEntry
	sessionsMu sync.Mutex
)

func getSessionID(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie("session_id")
	if err == nil && c.Value != "" {
		return c.Value
	}
	b := make([]byte, 16)
	rand.Read(b)
	id := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     "session_id",
		Value:    id,
		Path:     "/",
		HttpOnly: true,
	})
	return id
}

func getSessionLimiters(sid string) map[string]*limiterEntry {
	v, ok := sessions.Load(sid)
	if ok {
		return v.(map[string]*limiterEntry)
	}
	m := make(map[string]*limiterEntry)
	sessions.Store(sid, m)
	return m
}

func configHash(cfg map[string]interface{}) string {
	b, _ := json.Marshal(cfg)
	return string(b)
}

func getInt64(cfg map[string]interface{}, key string, def int64) int64 {
	v, ok := cfg[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return def
}

func getString(cfg map[string]interface{}, key, def string) string {
	v, ok := cfg[key]
	if !ok {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

func createLimiter(algo string, cfg map[string]interface{}) (goratelimit.Limiter, error) {
	switch algo {
	case "fixed-window":
		return goratelimit.NewFixedWindow(
			getInt64(cfg, "maxRequests", 10),
			getInt64(cfg, "windowSeconds", 10),
		)
	case "sliding-window-log":
		return goratelimit.NewSlidingWindow(
			getInt64(cfg, "maxRequests", 10),
			getInt64(cfg, "windowSeconds", 10),
		)
	case "sliding-window-counter":
		return goratelimit.NewSlidingWindowCounter(
			getInt64(cfg, "maxRequests", 10),
			getInt64(cfg, "windowSeconds", 10),
		)
	case "token-bucket":
		return goratelimit.NewTokenBucket(
			getInt64(cfg, "maxTokens", 10),
			getInt64(cfg, "refillRate", 1),
		)
	case "leaky-bucket":
		mode := goratelimit.Policing
		if getString(cfg, "mode", "policing") == "shaping" {
			mode = goratelimit.Shaping
		}
		return goratelimit.NewLeakyBucket(
			getInt64(cfg, "capacity", 10),
			getInt64(cfg, "leakRate", 1),
			mode,
		)
	case "gcra":
		return goratelimit.NewGCRA(
			getInt64(cfg, "rate", 5),
			getInt64(cfg, "burst", 10),
		)
	}
	return nil, fmt.Errorf("unknown algorithm: %s", algo)
}

func getLimiter(sid, algo string, cfg map[string]interface{}) (goratelimit.Limiter, error) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()

	m := getSessionLimiters(sid)
	hash := configHash(cfg)

	if entry, ok := m[algo]; ok && entry.configHash == hash {
		return entry.limiter, nil
	}

	l, err := createLimiter(algo, cfg)
	if err != nil {
		return nil, err
	}
	m[algo] = &limiterEntry{limiter: l, configHash: hash}
	return l, nil
}

type apiResult struct {
	Allowed    bool     `json:"allowed"`
	Remaining  int64    `json:"remaining"`
	Limit      int64    `json:"limit"`
	RetryAfter *float64 `json:"retryAfter"`
	Delay      *float64 `json:"delay,omitempty"`
}

func toAPIResult(r *goratelimit.Result) apiResult {
	res := apiResult{
		Allowed:   r.Allowed,
		Remaining: r.Remaining,
		Limit:     r.Limit,
	}
	if r.RetryAfter > 0 {
		v := r.RetryAfter.Seconds()
		res.RetryAfter = &v
	}
	return res
}

func main() {
	funcMap := template.FuncMap{
		"toJSON": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
	}

	homeTmpl := template.Must(
		template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/home.html"),
	)
	algoTmpl := template.Must(
		template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/algorithm.html"),
	)

	staticContent, _ := fs.Sub(staticFS, "static")
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticContent))))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		homeTmpl.ExecuteTemplate(w, "layout", map[string]interface{}{
			"Algorithms": algorithms,
		})
	})

	http.HandleFunc("/api/rate-limit/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/rate-limit/")
		sid := getSessionID(w, r)

		if r.Method == http.MethodGet && strings.HasSuffix(path, "/view") {
			slug := strings.TrimSuffix(path, "/view")
			meta, ok := algoBySlug[slug]
			if !ok {
				http.Error(w, "unknown algorithm", 404)
				return
			}

			for i, f := range meta.ConfigFields {
				for j, o := range f.Options {
					meta.ConfigFields[i].Options[j].Selected = o.Value == fmt.Sprint(f.Default)
				}
			}

			defaults := make(map[string]interface{})
			for _, f := range meta.ConfigFields {
				defaults[f.Name] = f.Default
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			algoTmpl.ExecuteTemplate(w, "algorithm.html", map[string]interface{}{
				"Meta":          meta,
				"DefaultConfig": defaults,
			})
			return
		}

		if r.Method == http.MethodPost && path == "reset" {
			sessionsMu.Lock()
			sessions.Delete(sid)
			sessionsMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			return
		}

		if r.Method == http.MethodPost {
			isBurst := strings.HasSuffix(path, "/burst")
			slug := path
			if isBurst {
				slug = strings.TrimSuffix(path, "/burst")
			}

			if _, ok := algoBySlug[slug]; !ok {
				http.Error(w, `{"error":"unknown algorithm"}`, 400)
				return
			}

			var body struct {
				Config map[string]interface{} `json:"config"`
				Count  int                    `json:"count"`
			}
			json.NewDecoder(r.Body).Decode(&body)

			if body.Config == nil {
				body.Config = make(map[string]interface{})
			}
			if body.Count < 1 {
				body.Count = 5
			}
			if body.Count > 50 {
				body.Count = 50
			}

			limiter, err := getLimiter(sid, slug, body.Config)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), 500)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			ctx := context.Background()

			if isBurst {
				results := make([]apiResult, 0, body.Count)
				for i := 0; i < body.Count; i++ {
					res, err := limiter.Allow(ctx, "demo")
					if err != nil {
						http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), 500)
						return
					}
					results = append(results, toAPIResult(&res))
				}
				json.NewEncoder(w).Encode(results)
			} else {
				res, err := limiter.Allow(ctx, "demo")
				if err != nil {
					http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), 500)
					return
				}
				json.NewEncoder(w).Encode(toAPIResult(&res))
			}
			return
		}

		http.NotFound(w, r)
	})

	port := 8080
	log.Printf("Demo server running on http://localhost:%d", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}
