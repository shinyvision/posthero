package script

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
	"github.com/shinyvision/posthero/internal/send"
	"github.com/shinyvision/posthero/internal/vars"
)

type fixture struct {
	env     *Env
	local   *vars.Scope
	envVars *vars.Scope
	colVars *vars.Scope
	globals *vars.Scope
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		local:   &vars.Scope{Kind: vars.Local},
		envVars: &vars.Scope{Kind: vars.Environment, Name: "Shop - Local", Vars: []vars.Var{{Key: "BackendEndpoint", Value: "http://localhost:8080"}, {Key: "helpdesk_username", Value: "api"}, {Key: "helpdesk_password", Value: "secret"}, {Key: "who", Value: "env"}}},
		colVars: &vars.Scope{Kind: vars.Collection, Name: "Shop", Vars: []vars.Var{{Key: "companyNumber", Value: "12345678"}, {Key: "who", Value: "collection"}}},
		globals: &vars.Scope{Kind: vars.Global, Name: "Globals"},
	}
	req := &send.Request{
		Method: "POST",
		URL:    postman.Rebuild(nil, "{{BackendEndpoint}}/graphql?x=1", nil, nil),
		Header: postman.Params{{Key: "Origin", Value: "{{Origin}}"}},
		Body:   &postman.Body{Mode: "graphql", GraphQL: &postman.GraphQL{Query: "mutation { createLocation { id } }"}},
	}
	f.env = &Env{
		Vars:    vars.New(f.local, f.envVars, f.colVars, f.globals),
		Info:    Info{RequestName: "Create location", RequestID: "r1", Iteration: 0, IterationCount: 1},
		Request: req,
		Jar:     send.NewJar(""),
	}
	return f
}

func (f *fixture) respond(status int, body string, headers ...send.Header) {
	f.env.Sent = &send.Prepared{Method: "POST", URL: "http://localhost:8080/graphql?x=1", Header: []send.Header{{Name: "Origin", Value: "http://localhost:3000"}}}
	f.env.Response = &send.Response{Status: status, StatusText: http.StatusText(status), Body: []byte(body), Header: append([]send.Header{{Name: "Content-Type", Value: "application/json"}}, headers...), Time: 42 * time.Millisecond}
}

func run(t *testing.T, f *fixture, event, code string) (Result, error) {
	t.Helper()
	s, err := NewEngine().NewSession(context.Background(), f.env)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Run(code, event, []string{"Shop", "Locations"})
	return s.Result(), err
}

func passed(t *testing.T, r Result, want ...string) {
	t.Helper()
	var got []string
	for _, tt := range r.Tests {
		mark := "✓"
		if tt.Skipped {
			mark = "-"
		} else if !tt.Passed {
			mark = "✗"
		}
		got = append(got, mark+" "+tt.Name)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("tests:\n%s\nwant:\n%s\nerrors: %v %+v", strings.Join(got, "\n"), strings.Join(want, "\n"), r.Errors, r.Tests)
	}
}

// A collection's test script, as teams write them.
func TestCollectionScript(t *testing.T) {
	f := newFixture(t)
	f.respond(200, `{"data":{"createLocation":{"id":"loc-1","address":null,"effectiveManagedByGroup":{"id":"g1"}}}}`)
	f.colVars.Set("contactPersonGroupId", "g1")
	r, err := run(t, f, "test", `
const body = pm.response.json();
pm.test('No GraphQL errors', () => pm.expect(body.errors).to.be.undefined);
const location = body.data?.createLocation;
if (location) {
    pm.collectionVariables.set('locationId', location.id);
    console.log(`+"`locationId set to ${location.id}`"+`);
    pm.test('It has no address of its own', () => pm.expect(location.address).to.be.null);
    pm.test('It inherits the managing group of the building', () => pm.expect(location.effectiveManagedByGroup.id).to.eql(pm.collectionVariables.get('contactPersonGroupId')));
}
pm.test('Rejected as duplicate e-mail address', () => {
    pm.expect(JSON.stringify(body.errors)).to.include('USER_WITH_EMAIL_ALREADY_EXISTS');
});
`)
	if err != nil {
		t.Fatal(err)
	}
	passed(t, r, "✓ No GraphQL errors", "✓ It has no address of its own", "✓ It inherits the managing group of the building", "✗ Rejected as duplicate e-mail address")
	if v, _ := f.colVars.Get("locationId"); v.Value != "loc-1" || !r.Changed[vars.Collection] {
		t.Errorf("collection variable: %+v", v)
	}
	if len(r.Logs) != 1 || r.Logs[0].Text != "locationId set to loc-1" {
		t.Errorf("logs: %+v", r.Logs)
	}
	// JSON.stringify(undefined) is undefined, which chai won't search, as in
	// Postman.
	if !strings.HasPrefix(r.Tests[3].Error, "AssertionError: the given combination of arguments (undefined and string) is invalid") {
		t.Errorf("failure message: %q", r.Tests[3].Error)
	}
}

// A pre-request script that builds a Basic auth header with Node's Buffer.
func TestBufferScript(t *testing.T) {
	f := newFixture(t)
	_, err := run(t, f, "prerequest", `
const username = pm.environment.get("helpdesk_username");
const password = pm.environment.get("helpdesk_password");

const auth = Buffer.from(`+"`${username}:${password}`"+`).toString("base64");
pm.environment.set("helpdesk_auth_header", `+"`Basic ${auth}`"+`);
`)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.envVars.Get("helpdesk_auth_header"); v.Value != "Basic YXBpOnNlY3JldA==" {
		t.Errorf("header: %q", v.Value)
	}
}

func TestLegacySandbox(t *testing.T) {
	f := newFixture(t)
	f.respond(201, `{"access_token":"tok","data":{"issueTenantToken":{"token":"tt"}}}`)
	r, err := run(t, f, "test", `
var jsonData = JSON.parse(responseBody);
pm.environment.set("Messaging bearer token", jsonData.access_token);
postman.setEnvironmentVariable("TenantToken", jsonData.data.issueTenantToken.token);
tests["Status code is 201"] = responseCode.code === 201;
tests["Has a token"] = !!environment["Messaging bearer token"] || true;
tests["Fails"] = false;
const CryptoJS = require('crypto-js');
function base64(data) { return CryptoJS.enc.Base64.stringify(CryptoJS.enc.Utf8.parse(data)); }
pm.environment.set("IssuedVPEncoded", base64(pm.response.text()));
pm.globals.set("sha", CryptoJS.SHA256("abc").toString());
`)
	if err != nil {
		t.Fatal(err)
	}
	passed(t, r, "✓ Status code is 201", "✓ Has a token", "✗ Fails")
	for k, want := range map[string]string{"Messaging bearer token": "tok", "TenantToken": "tt"} {
		if v, _ := f.envVars.Get(k); v.Value != want {
			t.Errorf("%s = %q", k, v.Value)
		}
	}
	if v, _ := f.globals.Get("sha"); v.Value != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("sha: %q", v.Value)
	}
	if v, _ := f.envVars.Get("IssuedVPEncoded"); !strings.HasPrefix(v.Value, "eyJhY2Nlc3NfdG9rZW4iOiJ0b2si") {
		t.Errorf("base64: %q", v.Value)
	}
}

func TestResponseAssertions(t *testing.T) {
	f := newFixture(t)
	f.respond(200, `{"rows":[{"name":"b"},{"name":"a"}],"total":2}`, send.Header{Name: "X-Request-Id", Value: "abc"})
	f.env.Response.Cookies = []*http.Cookie{{Name: "session", Value: "s1"}}
	r, err := run(t, f, "test", `
pm.test('status', () => pm.response.to.have.status(200));
pm.test('reason', () => pm.response.to.have.status('OK'));
pm.test('ok', () => pm.response.to.be.ok);
pm.test('success', () => pm.response.to.be.success);
pm.test('not an error', () => pm.response.to.not.be.error);
pm.test('json', () => pm.response.to.be.json);
pm.test('header', () => pm.response.to.have.header('x-request-id', 'abc'));
pm.test('json body path', () => pm.response.to.have.jsonBody('rows[0].name', 'b'));
pm.test('schema', () => pm.response.to.have.jsonSchema({type: 'object', required: ['rows', 'total'], properties: {total: {type: 'number'}}}));
pm.test('fast', () => pm.expect(pm.response.responseTime).to.be.below(200));
pm.test('cookie', () => pm.expect(pm.cookies.get('session')).to.equal('s1'));
pm.test('sorted alphabetically', () => {
    const names = pm.response.json().rows.map(r => r.name);
    const sorted = [...names].sort((a, b) => a.localeCompare(b));
    pm.expect(names).to.eql(sorted);
});
pm.test('wrong status', function () { pm.response.to.have.status(404); });
pm.test.skip('later', () => {});
pm.test('request seen', () => pm.expect(pm.request.url.toString()).to.equal('http://localhost:8080/graphql?x=1'));
pm.test('request header', () => pm.expect(pm.request.headers.get('origin')).to.equal('http://localhost:3000'));
pm.test('info', () => pm.expect(pm.info.requestName + '/' + pm.info.eventName).to.equal('Create location/test'));
`)
	if err != nil {
		t.Fatal(err)
	}
	passed(t, r, "✓ status", "✓ reason", "✓ ok", "✓ success", "✓ not an error", "✓ json", "✓ header", "✓ json body path", "✓ schema", "✓ fast", "✓ cookie", "✗ sorted alphabetically", "✗ wrong status", "- later", "✓ request seen", "✓ request header", "✓ info")
	if e := r.Tests[12].Error; e != "AssertionError: expected response to have status code 404 but got 200" {
		t.Errorf("status failure: %q", e)
	}
}

func TestVariablesAndRequestChanges(t *testing.T) {
	f := newFixture(t)
	_, err := run(t, f, "prerequest", `
pm.variables.set('page', 2);
pm.environment.set('count', 5);
if (pm.variables.get('who') !== 'env') throw new Error('who is ' + pm.variables.get('who'));
if (pm.environment.get('count') + 1 !== 6) throw new Error('count is a string');
if (pm.variables.replaceIn('{{BackendEndpoint}}/p/{{page}}') !== 'http://localhost:8080/p/2') throw new Error(pm.variables.replaceIn('{{BackendEndpoint}}/p/{{page}}'));
if (!/^[0-9a-f-]{36}$/.test(pm.variables.replaceIn('{{$guid}}'))) throw new Error('no guid');
pm.collectionVariables.unset('companyNumber');
pm.request.headers.add({key: 'X-Trace', value: '{{$guid}}'});
pm.request.headers.upsert({key: 'Origin', value: 'http://example.com'});
pm.request.url.addQueryParams('y=2');
pm.request.method = 'put';
pm.request.body.update({mode: 'raw', raw: JSON.stringify({a: 1}), options: {raw: {language: 'json'}}});
`)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.local.Get("page"); v.Value != "2" || v.Typed != int64(2) {
		t.Errorf("local: %+v", v)
	}
	if _, ok := f.colVars.Get("companyNumber"); ok {
		t.Error("unset didn't unset")
	}
	req := f.env.Request
	if req.Method != "PUT" || req.URL.Raw != "{{BackendEndpoint}}/graphql?x=1&y=2" || len(req.URL.Query) != 2 {
		t.Errorf("url: %s %s %+v", req.Method, req.URL.Raw, req.URL.Query)
	}
	if len(req.Header) != 2 || req.Header[0].Value != "http://example.com" || req.Header[1].Key != "X-Trace" {
		t.Errorf("headers: %+v", req.Header)
	}
	if req.Body.Mode != "raw" || req.Body.Raw != `{"a":1}` || req.Body.Language() != "json" {
		t.Errorf("body: %+v", req.Body)
	}
}

func TestSkipAndNext(t *testing.T) {
	f := newFixture(t)
	r, err := run(t, f, "prerequest", `
// Without a location this would really delete the contact person, so only run it while there is one
if (!pm.collectionVariables.get('rootLocationId')) {
    pm.execution.skipRequest();
}
pm.execution.setNextRequest('Get users');
`)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Skip || r.Next == nil || *r.Next != "Get users" {
		t.Errorf("skip %v next %v", r.Skip, r.Next)
	}
}

func TestAsyncAndSendRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"path": r.URL.Path, "method": r.Method, "auth": r.Header.Get("Authorization")})
	}))
	defer srv.Close()
	f := newFixture(t)
	client := send.NewClient(nil)
	f.env.Send = func(ctx context.Context, r *send.Request) (*send.Response, error) {
		p, err := r.Prepare(f.env.Vars, send.Options{})
		if err != nil {
			return nil, err
		}
		return client.Do(ctx, p)
	}
	f.envVars.Set("api", srv.URL)
	f.respond(200, `{}`)
	start := time.Now()
	r, err := run(t, f, "test", `
pm.sendRequest(pm.variables.get('api') + '/callback', (err, res) => {
    pm.test('callback', () => pm.expect(res.json().path).to.equal('/callback'));
});
const res = await pm.sendRequest({url: '{{api}}/awaited', method: 'POST', header: {Authorization: 'Bearer x'}, body: {mode: 'raw', raw: '{}'}});
pm.test('awaited', () => {
    pm.expect(res.code).to.equal(200);
    pm.expect(res.json()).to.deep.include({path: '/awaited', method: 'POST', auth: 'Bearer x'});
});
pm.test('done', function (done) {
    setTimeout(() => { pm.expect(1).to.equal(1); done(); }, 50);
});
await new Promise(r => setTimeout(r, 20));
pm.test('async fn', async () => {
    const r2 = await pm.sendRequest(pm.variables.get('api') + '/again');
    pm.expect(r2.json().path).to.equal('/again');
});
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tests) != 4 {
		t.Fatalf("tests: %+v %v", r.Tests, r.Errors)
	}
	for _, tt := range r.Tests {
		if !tt.Passed {
			t.Errorf("%s: %s", tt.Name, tt.Error)
		}
	}
	if time.Since(start) > 5*time.Second {
		t.Error("too slow")
	}
}

func TestErrorsAndLimits(t *testing.T) {
	f := newFixture(t)
	f.respond(200, `not json`)
	r, err := run(t, f, "test", `
const a = 1;
pm.response.json();
`)
	if err == nil || !strings.Contains(err.Error(), "isn't JSON") || !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error: %v", err)
	}
	if len(r.Errors) != 1 {
		t.Errorf("errors: %v", r.Errors)
	}
	f.env.Timeout = 200 * time.Millisecond
	_, err = run(t, f, "test", `while (true) {}`)
	if err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Errorf("loop: %v", err)
	}
	_, err = run(t, f, "test", `require('cheerio')`)
	if err == nil || !strings.Contains(err.Error(), "cheerio") {
		t.Errorf("cheerio: %v", err)
	}
}

func TestLibraries(t *testing.T) {
	f := newFixture(t)
	f.respond(200, `<root a="1"><item>x</item><item>y</item><name>n</name></root>`)
	r, err := run(t, f, "test", `
pm.test('lodash', () => pm.expect(_.chunk([1,2,3], 2)).to.eql([[1,2],[3]]));
pm.test('moment', () => pm.expect(require('moment')('2026-10-12').add(1, 'day').format('YYYY-MM-DD')).to.equal('2026-10-13'));
pm.test('uuid', () => pm.expect(require('uuid').v4()).to.match(/^[0-9a-f-]{36}$/));
pm.test('xml2Json', () => {
    const x = xml2Json(pm.response.text());
    pm.expect(x.root.$.a).to.equal('1');
    pm.expect(x.root.item).to.eql(['x', 'y']);
    pm.expect(x.root.name).to.equal('n');
});
pm.test('tv4', () => pm.expect(tv4.validate({a: 1}, {type: 'object'})).to.be.true);
pm.test('atob', () => pm.expect(atob(btoa('héllo'.replace('é','e')))).to.equal('hello'));
pm.test('URL', () => pm.expect(new URL('https://x.com/a?b=1').searchParams.get('b')).to.equal('1'));
pm.test('csv', () => pm.expect(require('csv-parse/lib/sync')('a,b\n1,"2,3"\n', {columns: true})).to.eql([{a: '1', b: '2,3'}]));
pm.test('querystring', () => pm.expect(require('querystring').parse('a=1&b=x+y')).to.eql({a: '1', b: 'x y'}));
pm.test('cookie jar', () => {
    const jar = pm.cookies.jar();
    jar.set('http://localhost:8080', 'theme', 'dark');
    pm.expect(jar.get('http://localhost:8080', 'theme')).to.equal('dark');
});
pm.test('sdk', () => pm.expect(new (require('postman-collection').Url)('http://a.com/b?c=1').getQueryString()).to.equal('c=1'));
console.log({nested: {list: [1, 2]}}, 'text', 3, null);
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range r.Tests {
		if !tt.Passed {
			t.Errorf("%s: %s", tt.Name, tt.Error)
		}
	}
	if len(r.Tests) != 11 {
		t.Errorf("%d tests", len(r.Tests))
	}
	if len(r.Logs) != 1 || !strings.Contains(r.Logs[0].Text, `"list": [`) || !strings.HasSuffix(r.Logs[0].Text, " text 3 null") {
		t.Errorf("console: %+v", r.Logs)
	}
}

func BenchmarkTestScript(b *testing.B) {
	e := NewEngine()
	for i := 0; i < b.N; i++ {
		f := &fixture{}
		f.env = &Env{Vars: vars.New(), Request: &send.Request{Method: "GET", URL: &postman.URL{Raw: "http://x"}}}
		f.env.Response = &send.Response{Status: 200, Body: []byte(`{"data":{}}`)}
		s, _ := e.NewSession(context.Background(), f.env)
		_ = s.Run(`const body = pm.response.json(); pm.test('No GraphQL errors', () => pm.expect(body.errors).to.be.undefined);`, "test", nil)
	}
}

// A pre-request script that doesn't touch pm.request sends the request as
// it was written, and one that does keeps a {{scheme}}:// URL.
func TestUntouchedRequestStaysAsWritten(t *testing.T) {
	f := newFixture(t)
	f.env.Request.URL = postman.Rebuild(nil, "{{scheme}}://{{host}}/x", nil, nil)
	before := f.env.Request.URL
	if _, err := run(t, f, "prerequest", `pm.environment.set('a', 1);`); err != nil {
		t.Fatal(err)
	}
	if f.env.Request.URL != before {
		t.Errorf("an untouched request was written back: %s", f.env.Request.URL.Raw)
	}
	if _, err := run(t, f, "prerequest", `pm.request.headers.add({key: 'X-A', value: '1'});`); err != nil {
		t.Fatal(err)
	}
	if raw := f.env.Request.URL.Raw; raw != "{{scheme}}://{{host}}/x" {
		t.Errorf("url: %q", raw)
	}
	if len(f.env.Request.Header) != 2 {
		t.Errorf("headers: %+v", f.env.Request.Header)
	}
}

// authValue is the text of one auth parameter.
func authValue(a *postman.Auth, typ, key string) string {
	if a == nil {
		return "<no auth>"
	}
	for _, p := range a.Params[typ] {
		if p.Key == key {
			var s string
			_ = json.Unmarshal(p.Value, &s)
			return s
		}
	}
	return "<missing>"
}

func TestAuthUpdate(t *testing.T) {
	cases := []struct {
		code, typ string
		want      map[string]string
	}{
		{`pm.request.auth.update({token: 'new'});`, "bearer", map[string]string{"token": "new"}},
		{`pm.request.authorizeUsing('bearer', {token: 'abc'});`, "bearer", map[string]string{"token": "abc"}},
		{`pm.request.authorizeUsing('basic', [{key: 'username', value: 'u'}, {key: 'password', value: 'p'}]);`, "basic", map[string]string{"username": "u", "password": "p"}},
		{`pm.request.authorizeUsing({type: 'apikey', apikey: [{key: 'key', value: 'k'}]});`, "apikey", map[string]string{"key": "k"}},
		{`pm.request.auth.update(new (require('postman-collection').PropertyList)(null, [{key: 'token', value: 'listed'}]));`, "bearer", map[string]string{"token": "listed"}},
	}
	for _, c := range cases {
		f := newFixture(t)
		f.env.Request.Auth = &postman.Auth{Type: "bearer", Params: map[string][]postman.AuthParam{
			"bearer": {{Key: "token", Value: json.RawMessage(`"old"`), Type: "string"}},
		}}
		if _, err := run(t, f, "prerequest", c.code); err != nil {
			t.Fatalf("%s: %v", c.code, err)
		}
		a := f.env.Request.Auth
		if a.Type != c.typ {
			t.Errorf("%s: type %q", c.code, a.Type)
		}
		for k, want := range c.want {
			if got := authValue(a, c.typ, k); got != want {
				t.Errorf("%s: %s = %q, want %q", c.code, k, got, want)
			}
		}
	}
}

// A throw in pm.sendRequest's callback fails the script, as in Postman.
func TestSendRequestCallbackError(t *testing.T) {
	f := newFixture(t)
	f.env.Send = func(ctx context.Context, r *send.Request) (*send.Response, error) {
		return &send.Response{Status: 200, StatusText: "OK", Body: []byte(`{}`)}, nil
	}
	f.respond(200, `{}`)
	r, err := run(t, f, "test", `
const res = await pm.sendRequest('http://example.com/a');
pm.test('promise', () => pm.expect(res.code).to.equal(200));
pm.sendRequest('http://example.com/b', (err, res) => { throw new Error('in the callback'); });
`)
	if err == nil || !strings.Contains(err.Error(), "in the callback") || len(r.Errors) != 1 {
		t.Errorf("error: %v %v", err, r.Errors)
	}
	passed(t, r, "✓ promise")
	// Without a way to send, the callback gets the error, and a throw there
	// fails the script too.
	f.env.Send = nil
	_, err = run(t, f, "test", `pm.sendRequest('http://example.com/c', (err) => { throw new Error('got ' + err); });`)
	if err == nil || !strings.Contains(err.Error(), "got ") {
		t.Errorf("no sender: %v", err)
	}
}

// What a failed script left pending dies with it.
func TestTimersDontOutliveTheirScript(t *testing.T) {
	f := newFixture(t)
	f.env.Timeout = time.Second
	s, err := NewEngine().NewSession(context.Background(), f.env)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Run(`
setTimeout(() => { throw new Error('boom'); }, 1);
setTimeout(() => pm.environment.set('leak', 'yes'), 300);
setTimeout(() => {}, 10000);
`, "test", nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("first script: %v", err)
	}
	start := time.Now()
	if err := s.Run(`await new Promise(() => {});`, "test", nil); err != nil {
		t.Errorf("second script: %v", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("the second script waited %s for the first one's timers", d)
	}
	if err := s.Run(`await new Promise(r => setTimeout(r, 400));`, "test", nil); err != nil {
		t.Errorf("third script: %v", err)
	}
	if v, ok := f.envVars.Get("leak"); ok {
		t.Errorf("a timer of the failed script ran later: leak = %q", v.Value)
	}
}

func TestResponseBodies(t *testing.T) {
	f := newFixture(t)
	f.respond(200, "\xef\xbb\xbf{\"a\":1}")
	r, err := run(t, f, "test", `
pm.test('bom', () => pm.expect(pm.response.json().a).to.equal(1));
pm.test('strict', () => pm.expect(() => pm.response.json(null, true)).to.throw());
`)
	if err != nil {
		t.Fatal(err)
	}
	passed(t, r, "✓ bom", "✓ strict")

	for _, c := range []struct{ ct, body, want string }{
		{"text/plain; charset=iso-8859-1", "caf\xe9", "café"},
		{"text/plain; charset=latin1", "caf\xe9", "café"},
		{"text/plain; charset=windows-1252", "\x80 caf\xe9", "€ café"},
		{"application/json; charset=UTF-16LE", "{\x00\"\x00a\x00\"\x00:\x00\"\x00\xe9\x00\"\x00}\x00", `{"a":"é"}`},
		{"application/json; charset=utf-16", "\xfe\xff\x00{\x00}", `{}`},
		{"text/plain; charset=utf-8", "caf\xc3\xa9", "café"},
		{"text/plain", "caf\xc3\xa9", "café"},
		{"text/plain; charset=nonsense", "caf\xc3\xa9", "café"},
	} {
		f := newFixture(t)
		f.respond(200, c.body)
		f.env.Response.Header = []send.Header{{Name: "Content-Type", Value: c.ct}}
		r, err := run(t, f, "test", `pm.test('text', () => pm.expect(pm.response.text()).to.equal(`+jsString(c.want)+`)); pm.test('legacy', () => pm.expect(responseBody).to.equal(pm.response.text()));`)
		if err != nil {
			t.Fatal(err)
		}
		for _, tt := range r.Tests {
			if !tt.Passed {
				t.Errorf("%s: %s: %s", c.ct, tt.Name, tt.Error)
			}
		}
	}
}

// jsString writes s as a JavaScript string.
func jsString(s string) string { b, _ := json.Marshal(s); return string(b) }

// pm.request.url, headers and body may be given new values outright.
func TestAssignRequestParts(t *testing.T) {
	f := newFixture(t)
	_, err := run(t, f, "prerequest", `
pm.request.url = 'https://example.com/new?q=1';
pm.request.headers = [{key: 'A', value: '1'}];
pm.request.body = 'hi';
`)
	if err != nil {
		t.Fatal(err)
	}
	req := f.env.Request
	if req.URL.Raw != "https://example.com/new?q=1" || len(req.URL.Query) != 1 {
		t.Errorf("url: %+v", req.URL)
	}
	if len(req.Header) != 1 || req.Header[0].Key != "A" || req.Header[0].Value != "1" {
		t.Errorf("headers: %+v", req.Header)
	}
	if req.Body == nil || req.Body.Mode != "raw" || req.Body.Raw != "hi" {
		t.Errorf("body: %+v", req.Body)
	}
}

func TestSyntaxErrorLine(t *testing.T) {
	f := newFixture(t)
	f.respond(200, `{}`)
	for code, want := range map[string]string{
		"const a = 1;\nconst b = ;\n":  "SyntaxError: Unexpected token ;\n    at line 2, column 11",
		"let a = 1;\n\n  let a = 2;\n": "SyntaxError: Identifier 'a' has already been declared\n    at line 3, column 7",
	} {
		r, err := run(t, f, "test", code)
		if err == nil || err.Error() != want || len(r.Errors) != 1 {
			t.Errorf("error: %v, want %q", err, want)
		}
	}
}

func TestSandboxLibraries(t *testing.T) {
	f := newFixture(t)
	f.respond(200, `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><m:Price xmlns:m="urn:x" m:cur="EUR">12</m:Price></soap:Body></soap:Envelope>`)
	r, err := run(t, f, "test", `
pm.test('xml2Json keeps prefixes', () => {
    const body = xml2Json(responseBody)['soap:Envelope']['soap:Body'];
    pm.expect(body['m:Price']).to.eql({$: {'xmlns:m': 'urn:x', 'm:cur': 'EUR'}, _: '12'});
});
pm.test('expect.fail', () => pm.expect.fail('on purpose'));
pm.test('expect still works', () => pm.expect(1).to.equal(1));
pm.test('assert.deepEqual ignores key order', () => require('assert').deepEqual({a: 1, b: [1, {c: 2, d: 3}]}, {b: [1, {d: 3, c: 2}], a: 1}));
pm.test('assert.deepEqual still fails', () => pm.expect(() => require('assert').deepEqual({a: 1}, {a: 2})).to.throw());
pm.test('xml2js gives lists', () => {
    const xml2js = require('xml2js');
    let got;
    xml2js.parseString('<root a="1"><item>x</item><name>n</name></root>', (err, res) => { got = res; });
    pm.expect(got).to.eql({root: {$: {a: '1'}, item: ['x'], name: ['n']}});
    new xml2js.Parser({explicitArray: false}).parseString('<root><item>x</item></root>', (err, res) => { got = res; });
    pm.expect(got).to.eql({root: {item: 'x'}});
    xml2js.parseString('<root><item> x </item></root>', {explicitRoot: false, trim: true}, (err, res) => { got = res; });
    pm.expect(got).to.eql({item: ['x']});
});
pm.test('xml2Json keeps Postman\'s options', () => pm.expect(xml2Json('<root><item> x </item><item>y</item><one>z</one></root>')).to.eql({root: {item: ['x', 'y'], one: 'z'}}));
pm.test('url.parse', () => {
    const u = require('url').parse('https://user:pw@Example.com:8443/a/b?x=1&y=2#top', true);
    pm.expect([u.protocol, u.auth, u.hostname, u.port, u.host, u.pathname, u.search, u.hash, u.path]).to.eql(['https:', 'user:pw', 'example.com', '8443', 'example.com:8443', '/a/b', '?x=1&y=2', '#top', '/a/b?x=1&y=2']);
    pm.expect(u.query).to.eql({x: '1', y: '2'});
    pm.expect(require('url').parse('http://example.com').pathname).to.equal('/');
    pm.expect(require('url').format({protocol: 'https', hostname: 'example.com', pathname: '/p', query: {a: 1}})).to.equal('https://example.com/p?a=1');
    pm.expect(require('url').resolve('http://example.com/a/b', '../c')).to.equal('http://example.com/c');
    pm.expect(require('url').resolve('/one/two', 'three')).to.equal('/one/three');
    pm.expect(new (require('url').URL)('http://example.com/?a=1').searchParams.get('a')).to.equal('1');
    pm.expect(new URL('three', 'http://x/one/two').href).to.equal('http://x/one/three');
    pm.expect(new URL('../a?b=1#c', new URL('https://h.example/p/q/r')).href).to.equal('https://h.example/p/a?b=1#c');
    pm.expect(new (require('url').URL)('three', 'http://x/one/two').href).to.equal('http://x/one/three');
    pm.expect(new URL('http://x/a') instanceof URL).to.equal(true);
    pm.expect(() => new URL('three', 'not a url')).to.throw();
});
pm.test('util.inspect', () => {
    const util = require('util');
    pm.expect(util.inspect({a: 1, b: 'x', c: [1, 2], d: {e: {f: {g: 1}}}})).to.equal("{ a: 1, b: 'x', c: [ 1, 2 ], d: { e: { f: [Object] } } }");
    pm.expect(util.format('%s=%d', 'a', 1, {b: 2})).to.equal('a=1 { b: 2 }');
});
pm.test('uuid', () => {
    const uuid = require('uuid');
    pm.expect(uuid()).to.match(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    pm.expect(uuid.v4()).to.match(/^[0-9a-f-]{36}$/);
});
`)
	if err != nil {
		t.Fatal(err)
	}
	passed(t, r, "✓ xml2Json keeps prefixes", "✗ expect.fail", "✓ expect still works", "✓ assert.deepEqual ignores key order", "✓ assert.deepEqual still fails", "✓ xml2js gives lists", "✓ xml2Json keeps Postman's options", "✓ url.parse", "✓ util.inspect", "✓ uuid")
	if len(r.Tests) > 1 && r.Tests[1].Error != "AssertionError: on purpose" {
		t.Errorf("expect.fail: %q", r.Tests[1].Error)
	}
}
