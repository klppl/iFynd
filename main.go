// iFynd finds underpriced iPhones on Tradera by comparing active fixed-price
// listings against historical sold prices per (model, storage) bucket.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/klppl/ifynd/internal/analyze"
	"github.com/klppl/ifynd/internal/classify"
	"github.com/klppl/ifynd/internal/notify"
	"github.com/klppl/ifynd/internal/store"
	"github.com/klppl/ifynd/internal/tradera"
)

// Category pairs a Tradera category id with the classifier family that
// understands its listings.
type Category struct {
	ID     int
	Family classify.Family
	// Filter is extra search facets. The phone and tablet categories are
	// Apple-only by definition; the laptop category holds every brand, so
	// the MacBook family filters on af-computer_brand=Apple.
	Filter url.Values
}

type Config struct {
	DBPath       string
	Interval     time.Duration
	HTTPAddr     string
	Categories   []Category
	ThresholdPct float64 // min % below reference to count as a hit
	MinSamples   int     // min sold records before trusting a bucket
	MinPrice     int     // SEK; listings below this are junk/scam, not phones
	Metric       analyze.Metric
	TrimPct      float64 // trimmed_mean only
	LookbackDays int     // sold history window for averages

	SoldWindowDays int // scrape sold pages until listings are older than this
	SoldMaxPages   int // hard cap per incremental run
	BackfillPages  int // page cap for the first run on an empty DB
	ActiveMaxPages int

	RequestDelay time.Duration
	UserAgent    string
	Notifier     string

	Public      bool   // GUI is reachable from the internet
	WebPassword string // required when Public; unlocks broken/exclude
}

func loadConfig() (Config, error) {
	cfg := Config{
		DBPath:    envStr("IFYND_DB_PATH", "ifynd.db"),
		HTTPAddr:  envStr("IFYND_HTTP_ADDR", ":8080"),
		UserAgent: envStr("IFYND_USER_AGENT", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"),
		Notifier:  envStr("IFYND_NOTIFIER", "log"),
	}
	var err error
	if cfg.Interval, err = envDuration("IFYND_INTERVAL", 30*time.Minute); err != nil {
		return cfg, err
	}
	if cfg.RequestDelay, err = envDuration("IFYND_REQUEST_DELAY", 1500*time.Millisecond); err != nil {
		return cfg, err
	}
	if cfg.Categories, err = parseCategories(envStr("IFYND_CATEGORIES", "340186:iphone,342496:ipad,302393:macbook")); err != nil {
		return cfg, err
	}
	if cfg.ThresholdPct, err = envFloat("IFYND_THRESHOLD_PCT", 15); err != nil {
		return cfg, err
	}
	if cfg.MinSamples, err = envInt("IFYND_MIN_SAMPLES", 5); err != nil {
		return cfg, err
	}
	if cfg.MinPrice, err = envInt("IFYND_MIN_PRICE", 100); err != nil {
		return cfg, err
	}
	if cfg.TrimPct, err = envFloat("IFYND_TRIM_PCT", 10); err != nil {
		return cfg, err
	}
	if cfg.LookbackDays, err = envInt("IFYND_LOOKBACK_DAYS", 90); err != nil {
		return cfg, err
	}
	if cfg.SoldWindowDays, err = envInt("IFYND_SOLD_WINDOW_DAYS", 14); err != nil {
		return cfg, err
	}
	if cfg.SoldMaxPages, err = envInt("IFYND_SOLD_MAX_PAGES", 20); err != nil {
		return cfg, err
	}
	if cfg.BackfillPages, err = envInt("IFYND_BACKFILL_PAGES", 100); err != nil {
		return cfg, err
	}
	if cfg.ActiveMaxPages, err = envInt("IFYND_ACTIVE_MAX_PAGES", 25); err != nil {
		return cfg, err
	}
	if cfg.Metric, err = analyze.ParseMetric(envStr("IFYND_METRIC", "median")); err != nil {
		return cfg, err
	}
	if cfg.Public, err = envBool("IFYND_PUBLIC", false); err != nil {
		return cfg, err
	}
	cfg.WebPassword = envStr("IFYND_WEB_PASSWORD", "")
	if cfg.Public && cfg.WebPassword == "" {
		return cfg, fmt.Errorf("IFYND_PUBLIC requires IFYND_WEB_PASSWORD to be set")
	}
	return cfg, nil
}

// parseCategories parses "340186:iphone,342496:ipad".
func parseCategories(s string) ([]Category, error) {
	var out []Category
	for _, part := range strings.Split(s, ",") {
		id, fam, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			return nil, fmt.Errorf("IFYND_CATEGORIES: %q is not <id>:<family>", part)
		}
		n, err := strconv.Atoi(id)
		if err != nil {
			return nil, fmt.Errorf("IFYND_CATEGORIES: %w", err)
		}
		f, err := classify.ParseFamily(fam)
		if err != nil {
			return nil, fmt.Errorf("IFYND_CATEGORIES: %w", err)
		}
		c := Category{ID: n, Family: f}
		if f == classify.MacBook {
			c.Filter = url.Values{"af-computer_brand": {"Apple"}}
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("IFYND_CATEGORIES: no categories configured")
	}
	return out, nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

func envFloat(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func main() {
	once := flag.Bool("once", false, "run one scrape+compare cycle and exit")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open db", "path", cfg.DBPath, "err", err)
		os.Exit(1)
	}
	defer st.Close()

	notifier, err := notify.New(cfg.Notifier)
	if err != nil {
		slog.Error("notifier", "err", err)
		os.Exit(1)
	}

	app := &App{
		cfg:      cfg,
		store:    st,
		client:   tradera.NewClient(cfg.UserAgent, cfg.RequestDelay),
		notifier: notifier,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *once {
		if err := app.Run(ctx); err != nil {
			slog.Error("run", "err", err)
			os.Exit(1)
		}
		return
	}

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: app.Router()}
	go func() {
		slog.Info("http listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http", "err", err)
		}
	}()

	cats := make([]string, len(cfg.Categories))
	for i, c := range cfg.Categories {
		cats[i] = fmt.Sprintf("%d(%s)", c.ID, c.Family)
	}
	slog.Info("starting", "interval", cfg.Interval, "categories", strings.Join(cats, ","),
		"threshold_pct", cfg.ThresholdPct, "metric", string(cfg.Metric), "min_samples", cfg.MinSamples)

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		if err := app.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("run", "err", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			srv.Shutdown(shutCtx)
			cancel()
			return
		}
	}
}
