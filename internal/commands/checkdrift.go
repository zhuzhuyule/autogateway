package commands

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// RunCheckDrift compares the ofind FreeModels registry against the local
// freeProviders.ts FREE_PROVIDERS list and reports any drift (provider
// present on one side only, ID mismatch, channelType mismatch).
//
// Designed to be wired into CI as a non-zero exit when drift is detected,
// so adding a new provider to ofind triggers a PR comment in api-center
// instead of silently waiting for someone to notice the missing entry.
func RunCheckDrift(args []string) {
	fs := flag.NewFlagSet("check-freemodels-drift", flag.ExitOnError)
	failOnDrift := fs.Bool("fail-on-drift", false, "Exit 1 if any drift is detected (for CI)")
	url := fs.String("url", "https://ofind.cn/FreeModels/data/models.json", "ofind registry URL")
	path := fs.String("path", "web/src/data/freeProviders.ts", "Path to freeProviders.ts")
	verbose := fs.Bool("v", false, "Verbose: print per-provider details")

	fs.Usage = func() {
		fmt.Println("AutoGateway FreeModels Drift Checker")
		fmt.Println()
		fmt.Println("Compares ofind.cn registry against local FREE_PROVIDERS and reports")
		fmt.Println("providers present on only one side, plus channelType mismatches.")
		fmt.Println()
		fmt.Println("Usage:")
		fmt.Println("  autogateway check-freemodels-drift [--fail-on-drift] [-v]")
		fmt.Println()
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		fatal("parse args: %v", err)
	}

	registry, err := fetchOfind(*url)
	if err != nil {
		fatal("fetch ofind: %v", err)
	}
	locals, err := parseLocalProviders(*path)
	if err != nil {
		fatal("parse %s: %v", *path, err)
	}

	report := buildDriftReport(registry, locals)
	report.Render(os.Stdout, *verbose)

	if report.hasDrift() && *failOnDrift {
		os.Exit(1)
	}
}

// ----- ofind fetch ---------------------------------------------------------

type ofindEnvelope struct {
	Total     int                      `json:"total"`
	UpdatedAt string                   `json:"updated_at"`
	Providers map[string]ofindProvider `json:"providers"`
	Data      []ofindModel             `json:"data"`
}

type ofindProvider struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	APIBaseURL  string `json:"apiBaseUrl"`
	ChannelType string `json:"channelType"`
}

type ofindModel struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	IsFree   bool   `json:"is_free"`
}

func fetchOfind(url string) (*ofindEnvelope, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// ofind 拒 default Go user-agent — set a real one.
	req.Header.Set("User-Agent", "autogateway-drift-checker/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		return nil, err
	}
	var env ofindEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &env, nil
}

// ----- local freeProviders.ts parse ---------------------------------------

type localProvider struct {
	ID            string
	ChannelType   string
	BaseURL       string
	UpstreamHosts []string
	ModelCount    int // number of items in `models: [...]`
}

// parseLocalProviders extracts the FREE_PROVIDERS array from freeProviders.ts
// using a bracket-balanced scan + regex per entry. We don't need a full TS
// parser — the file follows a strict convention (one entry per object literal
// inside the top-level array).
func parseLocalProviders(path string) ([]localProvider, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	txt := string(raw)
	block, err := extractArrayBlock(txt, "export const FREE_PROVIDERS")
	if err != nil {
		return nil, err
	}
	return parseProviderEntries(block), nil
}

// extractArrayBlock returns the substring covering the `[...]` literal that
// follows the given declaration, balancing nested brackets correctly so an
// inner `{ a: [1, 2] }` doesn't terminate the outer array. Anchors the search
// to the `=` after the declaration so `: FreeProvider[]` type annotations
// don't mislead the bracket scanner.
func extractArrayBlock(txt, decl string) (string, error) {
	declAt := strings.Index(txt, decl)
	if declAt < 0 {
		return "", fmt.Errorf("declaration not found: %q", decl)
	}
	eqAt := strings.Index(txt[declAt:], "=")
	if eqAt < 0 {
		return "", fmt.Errorf("'=' not found after %q", decl)
	}
	openAt := strings.Index(txt[declAt+eqAt:], "[")
	if openAt < 0 {
		return "", fmt.Errorf("array '[' not found after %q", decl)
	}
	openAt += declAt + eqAt
	depth := 0
	inString := false
	var stringChar byte
	for i := openAt; i < len(txt); i++ {
		c := txt[i]
		if inString {
			if c == '\\' && i+1 < len(txt) {
				i++
				continue
			}
			if c == stringChar {
				inString = false
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			inString = true
			stringChar = c
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return txt[openAt : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("unterminated array starting at offset %d", openAt)
}

// Top-level entry splitter: walk the array body and yield each `{...}` that's
// at depth 1 (i.e. a direct child of the outer array). Nested objects (like
// `modelNames: { ... }`) stay inside their parent entry.
func splitTopLevelEntries(block string) []string {
	// block looks like "[ {...}, {...}, ]" — strip outer brackets.
	if len(block) < 2 || block[0] != '[' || block[len(block)-1] != ']' {
		return nil
	}
	inner := block[1 : len(block)-1]

	var out []string
	depth := 0
	start := -1
	inString := false
	var stringChar byte
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if inString {
			if c == '\\' && i+1 < len(inner) {
				i++
				continue
			}
			if c == stringChar {
				inString = false
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			inString = true
			stringChar = c
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, inner[start:i+1])
				start = -1
			}
		}
	}
	return out
}

var (
	reID          = regexp.MustCompile(`(?m)^\s*id:\s*"([^"]+)"`)
	reChannelType = regexp.MustCompile(`(?m)^\s*channelType:\s*"([^"]+)"`)
	reBaseURL     = regexp.MustCompile(`(?m)^\s*baseUrl:\s*"([^"]+)"`)
	reHostsBlock  = regexp.MustCompile(`(?ms)^\s*upstreamHosts:\s*\[([^\]]*)\]`)
	reHostItem    = regexp.MustCompile(`"([^"]+)"`)
	reModelsBlock = regexp.MustCompile(`(?ms)^\s*models:\s*\[([^\]]*)\]`)
)

func parseProviderEntries(block string) []localProvider {
	entries := splitTopLevelEntries(block)
	out := make([]localProvider, 0, len(entries))
	for _, e := range entries {
		lp := localProvider{}
		if m := reID.FindStringSubmatch(e); len(m) == 2 {
			lp.ID = m[1]
		} else {
			continue // skip entries without an id (shouldn't happen)
		}
		if m := reChannelType.FindStringSubmatch(e); len(m) == 2 {
			lp.ChannelType = m[1]
		}
		if m := reBaseURL.FindStringSubmatch(e); len(m) == 2 {
			lp.BaseURL = m[1]
		}
		if m := reHostsBlock.FindStringSubmatch(e); len(m) == 2 {
			for _, hm := range reHostItem.FindAllStringSubmatch(m[1], -1) {
				lp.UpstreamHosts = append(lp.UpstreamHosts, hm[1])
			}
		}
		if m := reModelsBlock.FindStringSubmatch(e); len(m) == 2 {
			lp.ModelCount = len(reHostItem.FindAllStringSubmatch(m[1], -1))
		}
		out = append(out, lp)
	}
	return out
}

// ----- ID alias map (keep in sync with web/src/api/freemodels.ts) ----------

// providerAliases maps an ID on either side to its peer on the other side.
// Same convention as PROVIDER_ALIAS_PAIRS in the frontend; one source of
// truth would be ideal but until we extract a shared schema package, the
// list is short enough to maintain by hand.
var providerAliases = map[string]string{
	"github":        "github-models", // ofind id → local id
	"github-models": "github",        // local id → ofind id
}

func canonicalID(id string) string {
	if alias, ok := providerAliases[id]; ok {
		// Use the lexicographically smaller one as the canonical key so both
		// sides land on the same bucket regardless of order.
		if alias < id {
			return alias
		}
	}
	return id
}

// ----- diff report --------------------------------------------------------

type providerStat struct {
	OfindID         string
	LocalID         string
	OfindChannel    string
	LocalChannel    string
	OfindBaseURL    string
	LocalBaseURL    string
	OfindModelCount int // free models on ofind side
	LocalModelCount int // entries in local `models: [...]`
}

type driftReport struct {
	OfindOnly      []*providerStat // in ofind, missing locally
	LocalOnly      []*providerStat // in local, missing on ofind (acceptable for non-FreeModels-covered providers)
	Both           []*providerStat // present on both — used for channelType / baseUrl mismatch checks
	ChannelMismatch []*providerStat
	BaseURLMismatch []*providerStat
	Updated        string
}

func (r *driftReport) hasDrift() bool {
	// "drift that needs human attention" = OfindOnly + ChannelMismatch.
	// LocalOnly is by design (huggingface/mistral/etc. aren't in ofind).
	// BaseURLMismatch is informational unless paths differ meaningfully.
	return len(r.OfindOnly) > 0 || len(r.ChannelMismatch) > 0
}

func buildDriftReport(env *ofindEnvelope, locals []localProvider) *driftReport {
	// Count free models per ofind provider.
	freeByProvider := map[string]int{}
	for _, m := range env.Data {
		if m.IsFree {
			freeByProvider[m.Provider]++
		}
	}

	// Index locals by canonical ID.
	byCanon := map[string]*providerStat{}

	for k, op := range env.Providers {
		canon := canonicalID(k)
		ps := byCanon[canon]
		if ps == nil {
			ps = &providerStat{}
			byCanon[canon] = ps
		}
		ps.OfindID = k
		ps.OfindChannel = op.ChannelType
		ps.OfindBaseURL = op.APIBaseURL
		ps.OfindModelCount = freeByProvider[k]
	}

	for _, lp := range locals {
		canon := canonicalID(lp.ID)
		ps := byCanon[canon]
		if ps == nil {
			ps = &providerStat{}
			byCanon[canon] = ps
		}
		ps.LocalID = lp.ID
		ps.LocalChannel = lp.ChannelType
		ps.LocalBaseURL = lp.BaseURL
		ps.LocalModelCount = lp.ModelCount
	}

	r := &driftReport{Updated: env.UpdatedAt}
	for _, ps := range byCanon {
		switch {
		case ps.OfindID == "":
			r.LocalOnly = append(r.LocalOnly, ps)
		case ps.LocalID == "":
			r.OfindOnly = append(r.OfindOnly, ps)
		default:
			r.Both = append(r.Both, ps)
			if ps.OfindChannel != "" && ps.LocalChannel != "" && ps.OfindChannel != ps.LocalChannel {
				r.ChannelMismatch = append(r.ChannelMismatch, ps)
			}
			if !baseURLsEquivalent(ps.OfindBaseURL, ps.LocalBaseURL) {
				r.BaseURLMismatch = append(r.BaseURLMismatch, ps)
			}
		}
	}
	sortByID := func(s []*providerStat) {
		sort.Slice(s, func(i, j int) bool {
			return sortKey(s[i]) < sortKey(s[j])
		})
	}
	sortByID(r.OfindOnly)
	sortByID(r.LocalOnly)
	sortByID(r.Both)
	sortByID(r.ChannelMismatch)
	sortByID(r.BaseURLMismatch)
	return r
}

func sortKey(p *providerStat) string {
	if p.LocalID != "" {
		return p.LocalID
	}
	return p.OfindID
}

// Equivalent if the hosts match (ignoring scheme + path). Lets
// "https://api.x.com/v1" match "https://api.x.com" without forcing perfect
// equality — we care about reverse-lookup correctness, not pretty strings.
func baseURLsEquivalent(a, b string) bool {
	if a == b {
		return true
	}
	return hostOnly(a) == hostOnly(b)
}

func hostOnly(u string) string {
	s := u
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	for i, c := range s {
		if c == '/' || c == '?' || c == '#' {
			s = s[:i]
			break
		}
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// ----- rendering ----------------------------------------------------------

func (r *driftReport) Render(w io.Writer, verbose bool) {
	fmt.Fprintf(w, "FreeModels drift report (ofind updated %s)\n", or(r.Updated, "n/a"))
	fmt.Fprintf(w, "  intersection: %d  ofind-only: %d  local-only: %d\n",
		len(r.Both), len(r.OfindOnly), len(r.LocalOnly))
	fmt.Fprintln(w)

	if len(r.OfindOnly) > 0 {
		fmt.Fprintln(w, "❗ Providers in ofind but missing from freeProviders.ts (action needed):")
		for _, p := range r.OfindOnly {
			fmt.Fprintf(w, "  + %s  (%d free models, channel=%s, baseUrl=%s)\n",
				p.OfindID, p.OfindModelCount, or(p.OfindChannel, "?"), or(p.OfindBaseURL, "?"))
		}
		fmt.Fprintln(w)
	}

	if len(r.ChannelMismatch) > 0 {
		fmt.Fprintln(w, "❗ channelType mismatch:")
		for _, p := range r.ChannelMismatch {
			fmt.Fprintf(w, "  %s: ofind=%s local=%s\n", or(p.LocalID, p.OfindID), p.OfindChannel, p.LocalChannel)
		}
		fmt.Fprintln(w)
	}

	if len(r.BaseURLMismatch) > 0 {
		fmt.Fprintln(w, "⚠ baseURL host mismatch (informational — may or may not be a problem):")
		for _, p := range r.BaseURLMismatch {
			fmt.Fprintf(w, "  %s\n    ofind: %s\n    local: %s\n",
				or(p.LocalID, p.OfindID), p.OfindBaseURL, p.LocalBaseURL)
		}
		fmt.Fprintln(w)
	}

	if len(r.LocalOnly) > 0 {
		fmt.Fprintln(w, "ℹ Providers in freeProviders.ts but not in ofind (fine — ofind doesn't cover everything):")
		for _, p := range r.LocalOnly {
			fmt.Fprintf(w, "  %s  (%d local models)\n", p.LocalID, p.LocalModelCount)
		}
		fmt.Fprintln(w)
	}

	if verbose && len(r.Both) > 0 {
		fmt.Fprintln(w, "Intersection model-count comparison (ofind free vs local listed):")
		for _, p := range r.Both {
			fmt.Fprintf(w, "  %-20s  ofind=%-4d  local=%-4d\n",
				or(p.LocalID, p.OfindID), p.OfindModelCount, p.LocalModelCount)
		}
	}

	if !r.hasDrift() {
		fmt.Fprintln(w, "✅ No actionable drift.")
	}
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(2)
}
