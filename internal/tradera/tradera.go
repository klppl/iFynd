// Package tradera fetches and parses Tradera category search pages.
//
// Tradera renders category pages with Next.js RSC. The full search result —
// including structured item attributes that never appear in the visible HTML —
// is embedded as JSON inside self.__next_f.push([1,"..."]) script chunks.
// We concatenate those chunks, locate the discover/receiveSearchResults
// action, and decode its items array. Pagination uses a
// paging=<page>.a<total>.s0 query parameter.
package tradera

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const BaseURL = "https://www.tradera.com"

// Item is one listing as embedded in the page payload. Fields we don't use
// (images, seller, shipping) are omitted.
type Item struct {
	ItemID           int64       `json:"itemId"`
	Price            int         `json:"price"`       // sold: final price; active auctions: current bid
	BuyNowPrice      int         `json:"buyNowPrice"` // fixed price; 0 for pure auctions
	ShortDescription string      `json:"shortDescription"`
	ItemURL          string      `json:"itemUrl"`
	ItemType         string      `json:"itemType"` // Auction | AuctionBin | PureBin
	TotalBids        int         `json:"totalBids"`
	StartDate        time.Time   `json:"startDate"`
	EndDate          time.Time   `json:"endDate"`
	IsActive         bool        `json:"isActive"`
	CategoryID       int         `json:"categoryId"`
	Attributes       []Attribute `json:"attributes"`
}

// Attribute is a structured listing attribute, e.g.
// {name: "mobile_model", values: ["iPhone 14 Pro Max"]}.
type Attribute struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// Attr returns the first value of the named attribute, or "".
func (it *Item) Attr(name string) string {
	for _, a := range it.Attributes {
		if a.Name == name && len(a.Values) > 0 {
			return a.Values[0]
		}
	}
	return ""
}

// FixedPrice returns the "köp nu" price for an active listing: buyNowPrice
// for AuctionBin (price would be the current bid), price for PureBin.
func (it *Item) FixedPrice() int {
	if it.BuyNowPrice > 0 {
		return it.BuyNowPrice
	}
	return it.Price
}

// SoldPrice returns what a sold listing actually went for. When an
// AuctionBin sells via buy-now before any bid lands, the search payload's
// price field still holds the opening bid (often 1 kr) — the sale price is
// buyNowPrice. With bids, price is the winning bid.
func (it *Item) SoldPrice() int {
	if it.TotalBids == 0 && it.BuyNowPrice > 0 {
		return it.BuyNowPrice
	}
	return it.Price
}

// SearchResult is the decoded payload of one search page.
type SearchResult struct {
	Items          []Item
	TotalItemCount int
	PageCount      int
}

type Client struct {
	http      *http.Client
	userAgent string
	delay     time.Duration
}

func NewClient(userAgent string, delay time.Duration) *Client {
	return &Client{
		http:      &http.Client{Timeout: 30 * time.Second},
		userAgent: userAgent,
		delay:     delay,
	}
}

// Query identifies one category search.
type Query struct {
	CategoryID int
	Params     url.Values // itemStatus, sortBy, itemType ...
}

// SoldQuery lists sold items, newest listings first. Tradera offers no
// sort-by-end-date, but AddedOn descending bounds how deep a just-ended
// auction can sit: an item that sold today was listed at most ~2 weeks ago.
func SoldQuery(categoryID int) Query {
	return Query{CategoryID: categoryID, Params: url.Values{
		"itemStatus": {"Sold"},
		"sortBy":     {"AddedOn"},
	}}
}

// ActiveQuery lists active fixed-price ("köp nu") items, newest first.
func ActiveQuery(categoryID int) Query {
	return Query{CategoryID: categoryID, Params: url.Values{
		"itemStatus": {"Active"},
		"sortBy":     {"AddedOn"},
		"itemType":   {"FixedPrice"},
	}}
}

func (q Query) pageURL(page, total int) string {
	v := url.Values{}
	maps.Copy(v, q.Params)
	if page > 1 {
		v.Set("paging", fmt.Sprintf("%d.a%d.s0", page, total))
	}
	return fmt.Sprintf("%s/category/%d?%s", BaseURL, q.CategoryID, v.Encode())
}

// FetchPage fetches and parses one result page. page is 1-based; total is the
// totalItemCount from page 1 (ignored for page 1). The client sleeps its
// configured delay (with jitter) before every request after the first.
func (c *Client) FetchPage(ctx context.Context, q Query, page, total int) (*SearchResult, error) {
	u := q.pageURL(page, total)
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	res, err := ParseSearchPage(body)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", u, err)
	}
	return res, nil
}

func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("Accept-Language", "sv-SE,sv;q=0.9,en;q=0.8")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
			continue
		default:
			return nil, fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
		}
	}
	return nil, lastErr
}

// Throttle sleeps the configured inter-request delay plus up to 50% jitter.
func (c *Client) Throttle(ctx context.Context) error {
	d := c.delay + time.Duration(rand.Int63n(int64(c.delay)/2+1))
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var (
	nextFRe    = regexp.MustCompile(`self\.__next_f\.push\(\[1,("(?:[^"\\]|\\.)*")\]\)`)
	totalRe    = regexp.MustCompile(`"totalItemCount":(\d+)`)
	pageCntRe  = regexp.MustCompile(`"pageCount":(\d+)`)
	resultsKey = `receiveSearchResults`
)

// ParseSearchPage extracts the search result JSON from a category page.
func ParseSearchPage(html []byte) (*SearchResult, error) {
	var payload strings.Builder
	for _, m := range nextFRe.FindAllSubmatch(html, -1) {
		var s string
		if err := json.Unmarshal(m[1], &s); err != nil {
			continue // not a plain JSON string chunk
		}
		payload.WriteString(s)
	}
	p := payload.String()
	i := strings.Index(p, resultsKey)
	if i < 0 {
		return nil, fmt.Errorf("no %s payload found (flight data %d bytes)", resultsKey, len(p))
	}
	p = p[i:]

	j := strings.Index(p, `"items":[`)
	if j < 0 {
		return nil, fmt.Errorf(`no "items" array in search payload`)
	}
	dec := json.NewDecoder(strings.NewReader(p[j+len(`"items":`):]))
	var items []Item
	if err := dec.Decode(&items); err != nil {
		return nil, fmt.Errorf("decode items: %w", err)
	}

	res := &SearchResult{Items: items}
	if m := totalRe.FindStringSubmatch(p); m != nil {
		res.TotalItemCount, _ = strconv.Atoi(m[1])
	}
	if m := pageCntRe.FindStringSubmatch(p); m != nil {
		res.PageCount, _ = strconv.Atoi(m[1])
	}
	return res, nil
}
