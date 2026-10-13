package demo

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shinyvision/posthero/internal/auth/idptest"
)

// Product is a thing the shop sells.
type Product struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
	Stock int     `json:"stock"`
}

// Order is a user's order.
type Order struct {
	ID        string    `json:"id"`
	ProductID string    `json:"productId"`
	Quantity  int       `json:"quantity"`
	Note      string    `json:"note,omitempty"`
	Total     float64   `json:"total"`
	Created   time.Time `json:"created"`
	user      string
}

// Shop is the demo's API: products are public, orders need a token from
// the IdP.
type Shop struct {
	mu       sync.Mutex
	srv      *httptest.Server
	idp      *idptest.IdP
	products []Product
	orders   map[string]*Order
	seq      int
}

// NewShop returns the shop, its tokens checked by idp.
func NewShop(idp *idptest.IdP) *Shop {
	return &Shop{idp: idp, orders: map[string]*Order{}, products: []Product{
		{"p-1001", "Mechanical keyboard", 129, 14},
		{"p-1002", "Trackball", 89.5, 6},
		{"p-1003", "27\" monitor", 349, 3},
		{"p-1004", "USB-C dock", 179, 0},
		{"p-1005", "Desk lamp", 45, 22},
		{"p-1006", "Noise-cancelling headphones", 249, 9},
		{"p-1007", "Webcam", 69, 17},
		{"p-1008", "Standing desk", 499, 2},
	}}
}

// Start serves the shop and returns its URL.
func (s *Shop) Start() string {
	s.srv = httptest.NewServer(s.handler())
	return s.srv.URL
}

// Close stops serving.
func (s *Shop) Close() {
	if s.srv != nil {
		s.srv.Close()
	}
}

func (s *Shop) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/products", s.listProducts)
	mux.HandleFunc("GET /api/products/{id}", s.getProduct)
	mux.Handle("POST /api/orders", s.idp.Protect(http.HandlerFunc(s.createOrder)))
	mux.Handle("GET /api/orders", s.idp.Protect(http.HandlerFunc(s.listOrders)))
	mux.Handle("DELETE /api/orders/{id}", s.idp.Protect(http.HandlerFunc(s.cancelOrder)))
	mux.Handle("POST /graphql", s.idp.Protect(http.HandlerFunc(s.graphql)))
	mux.HandleFunc("/api/echo", echo)
	mux.HandleFunc("GET /api/basic", basic)
	mux.HandleFunc("GET /api/slow", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		select {
		case <-time.After(time.Duration(min(ms, 30000)) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"waited": ms})
	})
	mux.HandleFunc("GET /api/status/{code}", func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(r.PathValue("code"))
		if err != nil || code < 200 || code > 599 {
			code = http.StatusBadRequest
		}
		text := http.StatusText(code)
		if code == http.StatusTeapot {
			text = "I'm a teapot: short and stout."
		}
		writeJSON(w, code, map[string]any{"status": code, "message": text})
	})
	mux.HandleFunc("GET /api/feed.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><feed><title>New in the shop</title><entry><id>p-1007</id><title>Webcam</title></entry><entry><id>p-1008</id><title>Standing desk</title></entry></feed>`)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func problem(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": http.StatusText(status), "message": msg})
}

func (s *Shop) listProducts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	q := strings.ToLower(r.URL.Query().Get("q"))
	var out []Product
	for _, p := range s.products {
		if q == "" || strings.Contains(strings.ToLower(p.Name), q) {
			out = append(out, p)
		}
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(s.products)))
	writeJSON(w, http.StatusOK, out)
}

func (s *Shop) product(id string) (Product, bool) {
	for _, p := range s.products {
		if p.ID == id {
			return p, true
		}
	}
	return Product{}, false
}

func (s *Shop) getProduct(w http.ResponseWriter, r *http.Request) {
	p, ok := s.product(r.PathValue("id"))
	if !ok {
		problem(w, http.StatusNotFound, "no product "+r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// user is who the request's token belongs to.
func (s *Shop) user(r *http.Request) string {
	u, _ := s.idp.Valid(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	return u
}

func (s *Shop) newOrder(user, productID string, qty int, note string) (*Order, error) {
	p, ok := s.product(productID)
	switch {
	case !ok:
		return nil, fmt.Errorf("no product %q", productID)
	case qty < 1:
		return nil, fmt.Errorf("quantity must be at least 1")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	o := &Order{ID: fmt.Sprintf("o-%04d", 2000+s.seq), ProductID: p.ID, Quantity: qty, Note: note, Total: p.Price * float64(qty), Created: time.Now().UTC().Truncate(time.Second), user: user}
	s.orders[o.ID] = o
	return o, nil
}

func (s *Shop) createOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProductID string `json:"productId"`
		Quantity  int    `json:"quantity"`
		Note      string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		problem(w, http.StatusBadRequest, "the body isn't JSON: "+err.Error())
		return
	}
	o, err := s.newOrder(s.user(r), in.ProductID, in.Quantity, in.Note)
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.Header().Set("Location", "/api/orders/"+o.ID)
	writeJSON(w, http.StatusCreated, o)
}

func (s *Shop) ordersOf(user string) []*Order {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*Order{}
	for _, o := range s.orders {
		if o.user == user {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

func (s *Shop) listOrders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ordersOf(s.user(r)))
}

func (s *Shop) cancelOrder(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	o, ok := s.orders[r.PathValue("id")]
	if ok && o.user == s.user(r) {
		delete(s.orders, o.ID)
	}
	s.mu.Unlock()
	if !ok {
		problem(w, http.StatusNotFound, "no order "+r.PathValue("id"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// graphql answers the demo's queries. It isn't a GraphQL server: it looks
// at which fields are asked for, and answers with whole objects.
func (s *Shop) graphql(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errors": []any{map[string]any{"message": "the body isn't JSON: " + err.Error()}}})
		return
	}
	if strings.Contains(in.Query, "__schema") {
		writeJSON(w, http.StatusOK, introspection())
		return
	}
	user := s.user(r)
	data := map[string]any{}
	var errs []any
	has := func(field string) bool {
		return regexp.MustCompile(`[{\s]` + field + `\s*[({]`).MatchString(in.Query)
	}
	switch {
	case has("createOrder"):
		input, _ := in.Variables["input"].(map[string]any)
		id, _ := input["productId"].(string)
		qty, _ := input["quantity"].(float64)
		note, _ := input["note"].(string)
		o, err := s.newOrder(user, id, int(qty), note)
		if err != nil {
			errs = append(errs, map[string]any{"message": err.Error(), "path": []string{"createOrder"}})
			data["createOrder"] = nil
		} else {
			data["createOrder"] = o
		}
	default:
		if has("products") {
			first, _ := in.Variables["first"].(float64)
			list := s.products
			if first > 0 && int(first) < len(list) {
				list = list[:int(first)]
			}
			data["products"] = list
		}
		if has("product") {
			id, _ := in.Variables["id"].(string)
			if p, ok := s.product(id); ok {
				data["product"] = p
			} else {
				data["product"] = nil
			}
		}
		if has("me") {
			data["me"] = map[string]any{"username": user, "orders": s.ordersOf(user)}
		}
		if len(data) == 0 {
			errs = append(errs, map[string]any{"message": "the demo knows products, product(id), me and createOrder(input)"})
		}
	}
	out := map[string]any{"data": data}
	if len(errs) > 0 {
		out["errors"] = errs
	}
	writeJSON(w, http.StatusOK, out)
}

// echo says what it got, like postman-echo.
func echo(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	headers := map[string]string{}
	for k, v := range r.Header {
		headers[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	args := map[string]string{}
	for k, v := range r.URL.Query() {
		args[k] = strings.Join(v, ", ")
	}
	out := map[string]any{"method": r.Method, "url": r.URL.String(), "args": args, "headers": headers}
	switch ct := r.Header.Get("Content-Type"); {
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		vals, _ := url.ParseQuery(string(body))
		form := map[string]string{}
		for k, v := range vals {
			form[k] = strings.Join(v, ", ")
		}
		out["form"] = form
	case strings.HasPrefix(ct, "application/json"):
		var v any
		if json.Unmarshal(body, &v) == nil {
			out["json"] = v
		} else {
			out["data"] = string(body)
		}
	default:
		if len(body) > 0 {
			out["data"] = string(body)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// basic wants demo / s3cret.
func basic(w http.ResponseWriter, r *http.Request) {
	u, p, ok := r.BasicAuth()
	if !ok || u != "demo" || p != "s3cret" {
		w.Header().Set("WWW-Authenticate", `Basic realm="demo"`)
		problem(w, http.StatusUnauthorized, "log in as demo / s3cret")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "user": u})
}

// Orders are all the orders, of everyone.
func (s *Shop) Orders() []*Order {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*Order{}
	for _, o := range s.orders {
		out = append(out, o)
	}
	return out
}
