// Package demo is what posthero --demo runs against: a made-up team
// workspace (a fake Postman API), a fake Keycloak to log in to, and a small
// shop API with REST and GraphQL endpoints. Nothing real is read or
// changed.
package demo

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shinyvision/posthero/internal/auth/idptest"
	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/postman/pmtest"
)

// The demo's login, at its Keycloak.
const (
	User     = "ada"
	Password = "lovelace"
)

// Demo is the running demo.
type Demo struct {
	Postman *pmtest.Server
	IdP     *idptest.IdP
	Shop    *Shop
	// APIURL is the fake Postman API, APIKey the key it takes.
	APIURL, APIKey string
	// Workspace is the demo workspace's id.
	Workspace string
}

// Start starts the demo's servers.
func Start() (*Demo, error) {
	d := &Demo{IdP: idptest.New(), Postman: pmtest.New(), APIKey: pmtest.Key, Workspace: "ws1"}
	d.IdP.Realm = "shop"
	// Short-lived tokens, so the refreshing can be seen at work.
	d.IdP.AccessTTL = 2 * time.Minute
	idpURL := d.IdP.Start()
	d.IdP.AddUser(idptest.User{Name: User, Password: Password})
	d.IdP.AddClient(idptest.Client{ID: "shop-web", Redirects: []string{"http://localhost:3000/*"}})
	d.Shop = NewShop(d.IdP)
	shopURL := d.Shop.Start()
	d.APIURL = d.Postman.Start()

	for _, src := range []string{shopCollection, graphqlCollection} {
		var c postman.Collection
		if err := json.Unmarshal([]byte(src), &c); err != nil {
			d.Close()
			return nil, fmt.Errorf("demo collection: %w", err)
		}
		d.Postman.AddCollection(d.Workspace, &c)
	}
	env := func(name string, extra ...postman.EnvValue) *postman.Environment {
		return &postman.Environment{Name: name, Values: append([]postman.EnvValue{
			{Key: "baseUrl", Value: postman.Str(shopURL), Type: "default"},
			{Key: "keycloakUrl", Value: postman.Str(idpURL), Type: "default"},
			{Key: "realm", Value: "shop", Type: "default"},
			{Key: "basicUser", Value: "demo", Type: "default"},
			{Key: "basicPassword", Value: "s3cret", Type: "secret"},
		}, extra...)}
	}
	d.Postman.AddEnvironment(d.Workspace, env("Shop · Local"))
	d.Postman.AddEnvironment(d.Workspace, env("Shop · Production", postman.EnvValue{Key: "pageSize", Value: "3", Type: "default"}))
	d.Postman.SetGlobals(d.Workspace, []postman.EnvValue{{Key: "apiVersion", Value: "2026-10", Type: "default"}})
	return d, nil
}

// Close stops the servers.
func (d *Demo) Close() {
	if d.Shop != nil {
		d.Shop.Close()
	}
	d.IdP.Close()
	d.Postman.Close()
}

// Notice is what the status bar says when the demo starts.
func (d *Demo) Notice() string {
	return "demo: a made-up workspace; log in as " + User + " / " + Password + " when asked"
}

// oauth2 is the collections' auth: Keycloak's authorization code flow with
// PKCE, as teams set it up in Postman.
var oauth2 = strings.TrimSpace(`
{
  "type": "oauth2",
  "oauth2": [
    {"key": "grant_type", "value": "authorization_code_with_pkce", "type": "string"},
    {"key": "authUrl", "value": "{{keycloakUrl}}/realms/{{realm}}/protocol/openid-connect/auth", "type": "string"},
    {"key": "accessTokenUrl", "value": "{{keycloakUrl}}/realms/{{realm}}/protocol/openid-connect/token", "type": "string"},
    {"key": "clientId", "value": "shop-web", "type": "string"},
    {"key": "redirect_uri", "value": "http://localhost:3000/callback", "type": "string"},
    {"key": "scope", "value": "openid profile", "type": "string"},
    {"key": "client_authentication", "value": "body", "type": "string"},
    {"key": "challengeAlgorithm", "value": "S256", "type": "string"},
    {"key": "tokenName", "value": "shop", "type": "string"},
    {"key": "addTokenTo", "value": "header", "type": "string"},
    {"key": "useBrowser", "value": false, "type": "boolean"}
  ]
}`)

var shopCollection = `{
  "info": {
    "name": "Shop API",
    "description": "The shop's REST API. Products are public; orders need a login (Keycloak, set on the collection).",
    "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"
  },
  "auth": ` + oauth2 + `,
  "variable": [
    {"key": "pageSize", "value": "10"},
    {"key": "productId", "value": "p-1001"},
    {"key": "orderId", "value": ""}
  ],
  "event": [
    {"listen": "prerequest", "script": {"type": "text/javascript", "exec": [
      "// Every request gets an id, to find it in the logs.",
      "pm.request.headers.upsert({ key: 'X-Request-Id', value: pm.variables.replaceIn('{{$guid}}') });"
    ]}}
  ],
  "item": [
    {
      "name": "Products",
      "description": "The catalogue. No login needed.",
      "auth": {"type": "noauth"},
      "item": [
        {
          "name": "List products",
          "event": [{"listen": "test", "script": {"type": "text/javascript", "exec": [
            "pm.test('status is 200', () => pm.response.to.have.status(200));",
            "const products = pm.response.json();",
            "pm.test('a page of products', () => pm.expect(products).to.be.an('array').that.is.not.empty);",
            "pm.collectionVariables.set('productId', products[0].id);"
          ]}}],
          "request": {
            "method": "GET",
            "header": [{"key": "Accept", "value": "application/json"}, {"key": "X-Api-Version", "value": "{{apiVersion}}"}],
            "url": {
              "raw": "{{baseUrl}}/api/products?limit={{pageSize}}&q=",
              "host": ["{{baseUrl}}"], "path": ["api", "products"],
              "query": [{"key": "limit", "value": "{{pageSize}}"}, {"key": "q", "value": "", "disabled": true, "description": "search in the names"}]
            }
          }
        },
        {
          "name": "Get product",
          "event": [{"listen": "test", "script": {"type": "text/javascript", "exec": [
            "pm.test('status is 200', () => pm.response.to.have.status(200));",
            "pm.test('it has a price', () => pm.expect(pm.response.json().price).to.be.a('number'));"
          ]}}],
          "request": {
            "method": "GET",
            "header": [],
            "url": {
              "raw": "{{baseUrl}}/api/products/:id",
              "host": ["{{baseUrl}}"], "path": ["api", "products", ":id"],
              "variable": [{"key": "id", "value": "{{productId}}"}]
            }
          },
          "response": [
            {
              "name": "A product",
              "originalRequest": {"method": "GET", "header": [], "url": {"raw": "{{baseUrl}}/api/products/p-1001", "host": ["{{baseUrl}}"], "path": ["api", "products", "p-1001"]}},
              "status": "OK", "code": 200, "_postman_previewlanguage": "json",
              "header": [{"key": "Content-Type", "value": "application/json"}],
              "body": "{\n  \"id\": \"p-1001\",\n  \"name\": \"Mechanical keyboard\",\n  \"price\": 129.0,\n  \"stock\": 14\n}"
            }
          ]
        }
      ]
    },
    {
      "name": "Orders",
      "description": "Your orders. These log in with the collection's auth.",
      "item": [
        {
          "name": "Create order",
          "event": [{"listen": "test", "script": {"type": "text/javascript", "exec": [
            "pm.test('created', () => pm.response.to.have.status(201));",
            "const order = pm.response.json();",
            "pm.collectionVariables.set('orderId', order.id);",
            "console.log('order', order.id, 'for', order.total);"
          ]}}],
          "request": {
            "method": "POST",
            "header": [{"key": "Content-Type", "value": "application/json"}],
            "body": {"mode": "raw", "raw": "{\n  \"productId\": \"{{productId}}\",\n  \"quantity\": 2,\n  \"note\": \"{{$randomCatchPhrase}}\"\n}", "options": {"raw": {"language": "json"}}},
            "url": {"raw": "{{baseUrl}}/api/orders", "host": ["{{baseUrl}}"], "path": ["api", "orders"]}
          }
        },
        {
          "name": "My orders",
          "event": [{"listen": "test", "script": {"type": "text/javascript", "exec": [
            "pm.test('status is 200', () => pm.response.to.have.status(200));"
          ]}}],
          "request": {
            "method": "GET",
            "header": [],
            "url": {"raw": "{{baseUrl}}/api/orders", "host": ["{{baseUrl}}"], "path": ["api", "orders"]}
          }
        },
        {
          "name": "Cancel order",
          "event": [{"listen": "prerequest", "script": {"type": "text/javascript", "exec": [
            "if (!pm.collectionVariables.get('orderId')) {",
            "  console.warn('no order yet: send Create order first');",
            "  pm.execution.skipRequest();",
            "}"
          ]}}, {"listen": "test", "script": {"type": "text/javascript", "exec": [
            "pm.test('cancelled', () => pm.response.to.have.status(204));"
          ]}}],
          "request": {
            "method": "DELETE",
            "header": [],
            "url": {"raw": "{{baseUrl}}/api/orders/{{orderId}}", "host": ["{{baseUrl}}"], "path": ["api", "orders", "{{orderId}}"]}
          }
        }
      ]
    },
    {
      "name": "Tools",
      "auth": {"type": "noauth"},
      "item": [
        {
          "name": "Echo",
          "request": {
            "method": "POST",
            "header": [{"key": "X-Demo", "value": "yes"}],
            "body": {"mode": "urlencoded", "urlencoded": [{"key": "name", "value": "Ada"}, {"key": "lang", "value": "en"}, {"key": "debug", "value": "1", "disabled": true}]},
            "url": {"raw": "{{baseUrl}}/api/echo?hello=world", "host": ["{{baseUrl}}"], "path": ["api", "echo"], "query": [{"key": "hello", "value": "world"}]}
          }
        },
        {
          "name": "Basic auth",
          "request": {
            "auth": {"type": "basic", "basic": [{"key": "username", "value": "{{basicUser}}", "type": "string"}, {"key": "password", "value": "{{basicPassword}}", "type": "string"}]},
            "method": "GET",
            "header": [],
            "url": {"raw": "{{baseUrl}}/api/basic", "host": ["{{baseUrl}}"], "path": ["api", "basic"]}
          }
        },
        {
          "name": "Slow",
          "request": {
            "method": "GET",
            "header": [],
            "url": {"raw": "{{baseUrl}}/api/slow?ms=1500", "host": ["{{baseUrl}}"], "path": ["api", "slow"], "query": [{"key": "ms", "value": "1500"}]}
          }
        },
        {
          "name": "I'm a teapot",
          "request": {
            "method": "GET",
            "header": [],
            "url": {"raw": "{{baseUrl}}/api/status/418", "host": ["{{baseUrl}}"], "path": ["api", "status", "418"]}
          }
        },
        {
          "name": "XML feed",
          "request": {
            "method": "GET",
            "header": [{"key": "Accept", "value": "application/xml"}],
            "url": {"raw": "{{baseUrl}}/api/feed.xml", "host": ["{{baseUrl}}"], "path": ["api", "feed.xml"]}
          }
        }
      ]
    }
  ]
}`

var graphqlCollection = `{
  "info": {
    "name": "Shop GraphQL",
    "description": "The same shop, over GraphQL. Logs in like the Shop API (same Keycloak, same token).",
    "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"
  },
  "auth": ` + oauth2 + `,
  "item": [
    {
      "name": "Products",
      "event": [{"listen": "test", "script": {"type": "text/javascript", "exec": [
        "pm.test('no errors', () => pm.expect(pm.response.json().errors).to.be.undefined);"
      ]}}],
      "request": {
        "method": "POST",
        "header": [],
        "body": {"mode": "graphql", "graphql": {"query": "query Products($first: Int) {\n  products(first: $first) {\n    id\n    name\n    price\n  }\n}", "variables": "{\n  \"first\": 5\n}"}},
        "url": {"raw": "{{baseUrl}}/graphql", "host": ["{{baseUrl}}"], "path": ["graphql"]}
      }
    },
    {
      "name": "Product",
      "request": {
        "method": "POST",
        "header": [],
        "body": {"mode": "graphql", "graphql": {"query": "query Product($id: ID!) {\n  product(id: $id) {\n    id\n    name\n    price\n    stock\n  }\n}", "variables": "{\n  \"id\": \"p-1002\"\n}"}},
        "url": {"raw": "{{baseUrl}}/graphql", "host": ["{{baseUrl}}"], "path": ["graphql"]}
      }
    },
    {
      "name": "Me",
      "request": {
        "method": "POST",
        "header": [],
        "body": {"mode": "graphql", "graphql": {"query": "query Me {\n  me {\n    username\n    orders {\n      id\n      total\n    }\n  }\n}", "variables": ""}},
        "url": {"raw": "{{baseUrl}}/graphql", "host": ["{{baseUrl}}"], "path": ["graphql"]}
      }
    },
    {
      "name": "Place order",
      "request": {
        "method": "POST",
        "header": [],
        "body": {"mode": "graphql", "graphql": {"query": "mutation PlaceOrder($input: OrderInput!) {\n  createOrder(input: $input) {\n    id\n    total\n  }\n}", "variables": "{\n  \"input\": {\n    \"productId\": \"p-1003\",\n    \"quantity\": 1\n  }\n}"}},
        "url": {"raw": "{{baseUrl}}/graphql", "host": ["{{baseUrl}}"], "path": ["graphql"]}
      }
    }
  ]
}`
